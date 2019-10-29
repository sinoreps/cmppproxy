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

// Report is the data model for sms status reports
type Report struct {
	MessageID  uint64
	Mobile     string
	Status     string
	ReceivedAt int32
}

// ReportChannel is the channel handling reports
var ReportChannel = make(chan Report, 50000)

// SpawnReportWorker waits for incoming reports and sending callbacks
func SpawnReportWorker(workerID int) {
	for {
		data := <-ReportChannel
		log.Infof("processing report, workerID:%v, message_id:%v", workerID, data.MessageID)
		ReportCallback(data)
	}
}

// ReportCallback handles report sending to callback url
func ReportCallback(data Report) {

	callbackURL := os.Getenv("REPORT_CALLBACK_URL")

	if len(callbackURL) < 1 {
		log.Infof("report callback url is empty. message_id:%v", data.MessageID)
		return
	}

	payload, _ := json.Marshal([]map[string]interface{}{
		map[string]interface{}{
			"message_id":  fmt.Sprintf("%v", data.MessageID),
			"mobile":      data.Mobile,
			"status":      data.Status,
			"received_at": data.ReceivedAt,
		},
	})

	params := url.Values{
		"report": {string(payload)},
	}

	resp, err := http.PostForm(callbackURL, params)
	if err != nil {
		log.Errorf("report callback failed, url:%v, err:%v", callbackURL, err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)

	log.Infof("report callback finished, url:%v, request:%v, response:%v, status code:%v, err:%v", callbackURL, string(payload), string(respBody), resp.StatusCode, err)
}
