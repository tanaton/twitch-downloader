package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	CLIENT_ID               = "5mb1rkkrde9bnnm6d5q26pyw8rsosc" // 特に秘密にする必要は無さそう
	HTTP_TIMEOUT            = time.Second * 30
	COMMAND_TIMEOUT         = time.Second * 180
	DOWNLOAD_PARALLEL       = 4
	CHUNK_DOWNLOAD_PARALLEL = 4
)

type UserItem struct {
	Id string `json:"id"`
}
type Users struct {
	Data []UserItem `json:"data"`
}

type VideoItem struct {
	Id            string `json:"id"`
	numid         uint64
	User_id       string `json:"user_id"`
	User_name     string `json:"user_name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Created_at    string `json:"created_at"`
	Published_at  string `json:"published_at"`
	Url           string `json:"url"`
	Thumbnail_url string `json:"thumbnail_url"`
	Viewable      string `json:"viewable"`
	View_count    int    `json:"view_count"`
	Language      string `json:"language"`
	Type          string `json:"type"`
	Duration      string `json:"duration"`
}
type PageItem struct {
	Cursor string `json:"cursor"`
}
type Videos struct {
	Data       []VideoItem `json:"data"`
	Pagination PageItem    `json:"pagination"`
}

type Token struct {
	vid        string
	Token      string `json:"token"`
	Sig        string `json:"sig"`
	Expires_at string `json:"expires_at"`
}

var log *zap.SugaredLogger
var LocalClient = http.Client{
	Timeout: 30 * time.Second,
}
var regVideo = regexp.MustCompile("VIDEO=\"([^\"]+)\"\n([^\n]+)\n")

func init() {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	log = logger.Sugar()
}

func main() {
	if len(os.Args) < 2 {
		log.Warnw("引数にユーザー名を指定してね", "len", len(os.Args))
		os.Exit(1)
	}
	uname := os.Args[1]
	base := "."
	if len(os.Args) >= 3 {
		base = os.Args[2]
	}

	uid, err := getUserID(uname)
	if err != nil {
		log.Warnw("ユーザー名からユーザーIDの取得に失敗", "error", err)
		os.Exit(1)
	}
	vl, err := getVideoList(base, uid)
	if err != nil {
		log.Warnw("ビデオリストの取得に失敗", "error", err)
		os.Exit(1)
	}
	parallel := make(chan struct{}, DOWNLOAD_PARALLEL)
	for _, it := range vl.Data {
		parallel <- struct{}{}
		fmt.Println(it.Id, it.Title, it.Url, it.Duration)
		go func(v VideoItem) { // イテレーションしてる変数の参照を渡すとわけわからん感じになるので値渡しにする
			defer func() {
				<-parallel
			}()
			v.download(base)
		}(it)
	}
	for i := 0; i < DOWNLOAD_PARALLEL; i++ {
		parallel <- struct{}{}
	}
}

func getVideoList(base, uid string) (*Videos, error) {
	u, err := url.Parse("https://api.twitch.tv/helix/videos")
	if err != nil {
		log.Fatal(err)
	}
	q := u.Query()
	q.Set("user_id", uid)
	q.Set("first", "100")
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	req.Header.Add("Client-ID", CLIENT_ID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warnw("なんかエラーだって", "error", err)
		return nil, err
	}
	defer resp.Body.Close()

	v := Videos{}
	err = json.NewDecoder(resp.Body).Decode(&v)
	if err != nil {
		return nil, err
	}
	if len(v.Data) == 0 {
		return nil, errors.New("ビデオが無いっぽい")
	}
	vldata := []VideoItem{}
	for _, it := range v.Data {
		if isExist(it.getVideoPath(base)) == false {
			if s, err := strconv.ParseUint(it.Id, 10, 64); err == nil {
				it.numid = s
				vldata = append(vldata, it)
			}
		}
	}
	sort.Slice(vldata, func(i, j int) bool {
		return vldata[i].numid < vldata[j].numid
	})
	v.Data = vldata
	return &v, nil
}

func getUserID(uname string) (string, error) {
	u, err := url.Parse("https://api.twitch.tv/helix/users")
	if err != nil {
		log.Fatal(err)
	}
	q := u.Query()
	q.Set("login", uname)
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	req.Header.Add("Client-ID", CLIENT_ID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	user := Users{}
	err = json.NewDecoder(resp.Body).Decode(&user)
	if err != nil {
		return "", err
	}
	if len(user.Data) == 0 {
		return "", errors.New("存在しないユーザー名っぽい")
	}
	return user.Data[0].Id, nil
}

func (v *VideoItem) download(base string) {
	token, err := getToken(v.Id)
	if err != nil {
		log.Warnw("トークンの取得に失敗", "error", err)
		return
	}

	emap, err := token.getEdgecastURL()
	if err != nil {
		log.Warnw("APIアクセスに失敗", "error", err)
		return
	}

	m3u8Link, ok := emap["chunked"]
	if !ok {
		log.Warnw("チャンクが無い！", "url", m3u8Link)
		return
	}

	tslist, err := getTSList(m3u8Link)
	if err != nil {
		log.Warnw("ダウンロードリスト取得失敗", "error", err)
		return
	}

	ebase := m3u8Link[0:strings.LastIndex(m3u8Link, "/")] + "/"
	newpath := filepath.Join(base, "_"+v.Id)

	err = os.MkdirAll(newpath, os.ModePerm)
	if err != nil {
		log.Warnw("フォルダ作成失敗", "error", err)
		return
	}

	parallel := make(chan struct{}, CHUNK_DOWNLOAD_PARALLEL)
	for _, it := range tslist {
		parallel <- struct{}{}
		go func(cn string) {
			defer func() {
				<-parallel
			}()
			v.downloadChunk(newpath, ebase, cn)
		}(it)
	}
	for i := 0; i < CHUNK_DOWNLOAD_PARALLEL; i++ {
		parallel <- struct{}{}
	}

	v.ffmpegCombine(base, newpath, tslist)
	os.RemoveAll(newpath)
}

func (v *VideoItem) downloadChunk(newpath, ebase, cn string) {
	curl := ebase + cn
	dp := filepath.Join(newpath, cn)
	if isExist(dp) {
		// ファイルが存在する場合
		return
	}

	maxRetryCount := 3
	for retryCount := 0; retryCount < maxRetryCount; retryCount++ {
		if retryCount > 0 {
			log.Infow("チャンクダウンロードのリトライ", "count", retryCount, "name", cn)
		}

		err := func() error {
			resp, err := LocalClient.Get(curl)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				return errors.New("ステータスコードが変")
			}
			wfp, err := os.Create(dp)
			if err != nil {
				return err
			}
			defer wfp.Close()
			w := bufio.NewWriterSize(wfp, 512*1024)
			_, rerr := w.ReadFrom(resp.Body)
			if rerr != nil {
				return rerr
			}
			return w.Flush()
		}()

		if err == nil {
			break
		}
	}
}

func getToken(vid string) (*Token, error) {
	resp, err := http.Get("https://api.twitch.tv/api/vods/" + vid + "/access_token?&client_id=" + CLIENT_ID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var token Token
	err = json.NewDecoder(resp.Body).Decode(&token)
	token.vid = vid
	return &token, err
}

func (t *Token) getEdgecastURL() (map[string]string, error) {
	resp, err := http.Get("http://usher.twitch.tv/vod/" + t.vid + "?nauthsig=" + t.Sig + "&nauth=" + t.Token + "&allow_source=true")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	respString := string(body)
	match := regVideo.FindAllStringSubmatch(respString, -1)

	emap := make(map[string]string)
	for _, ele := range match {
		emap[ele[1]] = ele[2]
	}

	return emap, err
}

func getTSList(url string) ([]string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	list := []string{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		txt := scanner.Text()
		if len(txt) > 0 && txt[0] != '#' {
			list = append(list, txt)
		}
	}
	return list, nil
}

func (v *VideoItem) createConcatFile(newpath string, tslist []string) (*os.File, error) {
	tfp, err := ioutil.TempFile(newpath, "twitchVod_"+v.Id+"_")
	if err != nil {
		return nil, err
	}
	defer tfp.Close()

	concat := ""
	for _, it := range tslist {
		filePath, _ := filepath.Abs(filepath.Join(newpath, it))
		concat += "file '" + filePath + "'\n"
	}

	if _, err := tfp.WriteString(concat); err != nil {
		return nil, err
	}
	return tfp, nil
}

func (v *VideoItem) ffmpegCombine(base, newpath string, tslist []string) {
	ctx, cancel := context.WithTimeout(context.Background(), COMMAND_TIMEOUT)
	defer cancel()

	tfp, err := v.createConcatFile(newpath, tslist)
	if err != nil {
		log.Warnw("結合ファイルの生成に失敗", "error", err, "name", tfp.Name())
		return
	}
	defer os.Remove(tfp.Name())

	args := []string{
		"-f", "concat",
		"-safe", "0",
		"-i", tfp.Name(),
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-fflags", "+genpts",
		v.getVideoPath(base),
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var sbuf strings.Builder
	cmd.Stderr = &sbuf
	err = cmd.Run()
	if err != nil {
		log.Warnw("ffmpegの実行に失敗", "error", err, "log", sbuf.String())
	}
}

func (v *VideoItem) getVideoPath(base string) string {
	return filepath.Join(base, v.Id+"_"+v.Title+"_"+v.Duration+".mp4")
}

func isExist(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}
