package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Telegram struct {
	Token    string
	ChatID   string
	ThreadID int

	Client *http.Client
}

func NewTelegram(token, chatID string, threadID int) *Telegram {
	return &Telegram{
		Token:    token,
		ChatID:   chatID,
		ThreadID: threadID,
		Client:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (t *Telegram) SendPhotos(ctx context.Context, photos [][]byte, caption string) error {
	switch {
	case len(photos) == 0:
		return errors.New("telegram: no photos")
	case len(photos) == 1:
		return t.sendPhoto(ctx, photos[0], caption)
	case len(photos) > 10:
		photos = photos[:10]
	}
	return t.sendAlbum(ctx, photos, caption)
}

func (t *Telegram) sendPhoto(ctx context.Context, photo []byte, caption string) error {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	t.writeChat(w)
	w.WriteField("caption", caption)
	fw, err := w.CreateFormFile("photo", "snapshot.jpg")
	if err != nil {
		return err
	}
	fw.Write(photo)
	if err := w.Close(); err != nil {
		return err
	}
	return t.post(ctx, "sendPhoto", w.FormDataContentType(), body)
}

func (t *Telegram) sendAlbum(ctx context.Context, frames [][]byte, caption string) error {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	t.writeChat(w)

	type media struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption,omitempty"`
	}
	items := make([]media, 0, len(frames))
	for i, f := range frames {
		name := fmt.Sprintf("f%d", i)
		fw, err := w.CreateFormFile(name, name+".jpg")
		if err != nil {
			return err
		}
		fw.Write(f)
		m := media{Type: "photo", Media: "attach://" + name}
		if i == 0 {
			m.Caption = caption
		}
		items = append(items, m)
	}
	j, err := json.Marshal(items)
	if err != nil {
		return err
	}
	w.WriteField("media", string(j))
	if err := w.Close(); err != nil {
		return err
	}
	return t.post(ctx, "sendMediaGroup", w.FormDataContentType(), body)
}

func (t *Telegram) writeChat(w *multipart.Writer) {
	w.WriteField("chat_id", t.ChatID)
	if t.ThreadID != 0 {
		w.WriteField("message_thread_id", strconv.Itoa(t.ThreadID))
	}
}

func (t *Telegram) post(ctx context.Context, method, contentType string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.Token+"/"+method, body)
	if err != nil {
		return errors.New("telegram: invalid request")
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := t.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("telegram %s: %s: %s", method, resp.Status, bytes.TrimSpace(b))
	}
	return nil
}
