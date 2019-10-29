package callback

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"

	"github.com/sinoreps/cmppproxy/log"
)

// ReplyChannel is the channel for replies
var ReplyChannel = make(chan Reply, 5000)

// Reply is the definition of an SMS Reply model
type Reply struct {
	MessageID   uint64
	Mobile      string
	Content     string
	ExtensionNO string
	ReceivedAt  int32
}

// SpawnReplyWorker waits for incoming replies and send callbacks
func SpawnReplyWorker(workerID int) {
	for {
		data := <-ReplyChannel
		log.Infof("processing reply, workerID:%v, message_id:%v", workerID, data.MessageID)
		ReplyCallback(data)
	}
}

// ReplyCallback handles reply sending to callback url
func ReplyCallback(data Reply) {
	callbackURL := os.Getenv("REPLY_CALLBACK_URL")

	if len(callbackURL) < 1 {
		log.Infof("reply url is empty. message_id:%v", data.MessageID)
		return
	}

	payload, _ := json.Marshal([]map[string]interface{}{
		map[string]interface{}{
			"message_id":  fmt.Sprintf("%v", data.MessageID),
			"content":     data.Content,
			"mobile":      data.Mobile,
			"ext_no":      data.ExtensionNO,
			"received_at": data.ReceivedAt,
		},
	})

	params := url.Values{
		"reply": {string(payload)},
	}

	resp, err := http.PostForm(callbackURL, params)
	if err != nil {
		log.Errorf("reply callback failed, url:%v, err:%v", callbackURL, err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)

	log.Infof("reply callback finished, url:%v, request:%v, response:%v, status code:%v, err:%v", callbackURL, string(payload), string(respBody), resp.StatusCode, err)
}
