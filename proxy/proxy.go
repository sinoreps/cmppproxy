package proxy

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sinoreps/cmppproxy/callback"
	cmppclient "github.com/sinoreps/cmppproxy/cmpp/client"
	"github.com/sinoreps/cmppproxy/log"
)

// Proxy defines the basic fields to expose to callers
type Proxy struct {
	ID          string
	Account     string
	Password    string
	ServerAddr  string
	SourceID    string
	ServiceID   string
	CMPPVersion string
	OnReport    func(cmppclient.DeliveryReport)
	OnReply     func(cmppclient.Reply)

	client *cmppclient.CMPPClient
	once   sync.Once
}

// New creates a new Proxy instance
func New(id, serverAddr, account, password, sourceID, serviceID, cmppVersion string) *Proxy {
	c := Proxy{
		ID:          id,
		Account:     account,
		Password:    password,
		ServerAddr:  serverAddr,
		SourceID:    sourceID,
		ServiceID:   serviceID,
		CMPPVersion: cmppVersion,
	}
	c.OnReport = func(report cmppclient.DeliveryReport) {
		mobile := report.DestTerminalID
		status := report.Status
		recvTime := int32(time.Now().Unix())

		log.Infof("received report with message id: %v, mobile: %s, status: %v", report.MessageID, mobile, status)
		callback.ReportChannel <- callback.Report{
			MessageID:  report.MessageID,
			Mobile:     mobile,
			ReceivedAt: recvTime,
			Status:     status,
		}
	}
	c.OnReply = func(reply cmppclient.Reply) {
		messageID := reply.MessageID
		mobile := reply.SrcTerminalID
		content := reply.Content
		recvTime := int32(time.Now().Unix())
		extensionNO := strings.TrimPrefix(reply.DestID, sourceID)
		log.Infof("received reply with message id: %v, mobile: %s, content: %v, source id: %v", reply.MessageID, mobile, content, sourceID)
		callback.ReplyChannel <- callback.Reply{
			MessageID:   messageID,
			Mobile:      mobile,
			ReceivedAt:  recvTime,
			Content:     content,
			ExtensionNO: extensionNO,
		}
	}
	return &c
}

// Send is the module's entry method
func (c *Proxy) Send(mobile string, content string, extensionNO string, timeout time.Duration) (int, string, error) {
	sourceID := c.SourceID + extensionNO
	return c.sendRequest(c.ServerAddr, c.Account, c.Password, mobile, content, sourceID, c.ServiceID, timeout)
}

// InitIfNeeded initializes the client instance if it hasn't been initialized yet, and returns it
func (c *Proxy) InitIfNeeded() *cmppclient.CMPPClient {
	c.once.Do(func() {
		c.client = cmppclient.New(c.Account, c.Password, c.ServerAddr, c.ID, c.CMPPVersion, time.Second*10)
		c.client.OnReport = func(report cmppclient.DeliveryReport) {
			if c.OnReport != nil {
				c.OnReport(report)
			}
		}
		c.client.OnReply = func(reply cmppclient.Reply) {
			if c.OnReply != nil {
				c.OnReply(reply)
			}
		}
		c.client.Run()
	})
	return c.client
}

func calculateSmsCount(content string) int {
	count := utf8.RuneCountInString(content)
	if count <= 70 {
		return 1
	}

	return int(math.Ceil(float64(count) / 67))
}

func (c *Proxy) sendRequest(api, account, password, mobile, content, sourceID, serviceID string, timeout time.Duration) (int, string, error) {
	if timeout < 0 {
		timeout = time.Second * 30
	}
	client := c.InitIfNeeded()
	type Result struct {
		Error    error
		MessagID uint64
		Code     uint8
	}
	done := make(chan Result)
	callback := func(err error, messageID uint64, code uint8) {
		done <- Result{
			Error:    err,
			MessagID: messageID,
			Code:     code}
	}
	client.SendMessage(mobile, content, sourceID, serviceID, time.Now().Add(timeout), callback)

	select {
	case result := <-done:
		if result.Error != nil {
			log.Infof("cmppclient response error. err: %v", result.Error)
			return 0, "", result.Error
		}
		num := calculateSmsCount(content)
		return num, fmt.Sprintf("%v", result.MessagID), nil
	case <-time.NewTimer(timeout).C:
		log.Warn("cmppclient request timeout: ", mobile, content)
		return 0, "", errors.New("cmppclient request timeout")
	}
}
