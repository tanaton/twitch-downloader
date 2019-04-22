package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"go.uber.org/zap"
)

const (
	CLIENT_ID = "5mb1rkkrde9bnnm6d5q26pyw8rsosc" // 特に秘密にする必要は無さそう
)

type UserItem struct {
	Id string `json:"id"`
}
type Users struct {
	Data []UserItem `json:"data"`
}

type VideoItem struct {
	Id            string `json:"id"`
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

var log *zap.SugaredLogger

func init() {
	logger, err := zap.NewProduction()
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
	uid, err := getUserID(uname)
	if err != nil {
		log.Warnw("ユーザー名からユーザーIDの取得に失敗", "error", err)
		os.Exit(1)
	}
	vl, err := getVideoList(uid)
	if err != nil {
		log.Warnw("ビデオリストの取得に失敗", "error", err)
		os.Exit(1)
	}
	for _, it := range vl.Data {
		fmt.Println(it.Id, it.Title, it.Url, it.Duration)
	}
}

func getVideoList(uid string) (*Videos, error) {
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
