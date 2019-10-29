package client

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/sinoreps/cmppproxy/log"

	cmpp "github.com/bigwhite/gocmpp"
	cmpputils "github.com/bigwhite/gocmpp/utils"
	"github.com/jpillora/backoff"
	"github.com/karlseguin/ccache"
)

// Message is the model containing basic information to send an sms
type Message struct {
	Mobile      string
	Content     string
	SourceID    string
	ServiceID   string
	ValidBefore time.Time
	OnResult    func(error, uint64, uint8)
}

// Credentials specify the credentials used for login
type Credentials struct {
	Server   string
	User     string
	Password string
}

// DeliveryReport contains the parsed info from the DeliverRequestPacket
type DeliveryReport struct {
	MessageID      uint64
	Status         string
	DestTerminalID string
	SmscSequence   uint32
	SubmitTime     string
	DoneTime       string
}

// Reply contains the parsed info from the DeliverRequestPacket
type Reply struct {
	MessageID     uint64
	Content       string
	SrcTerminalID string
	DestID        string
}

// CMPPClient is the key model to handel connections and message exchange with the server
type CMPPClient struct {
	sync.RWMutex
	*Credentials

	ID                string
	Client            *cmpp.Client
	ClientVersion     cmpp.Type
	Quit              bool
	Connected         bool
	ConnectionTimeout time.Duration
	MessageChan       chan *Message
	PingChan          chan string
	ReconnectChan     chan int
	OnConnected       func()
	OnSubmitted       func()
	OnReport          func(DeliveryReport)
	OnReply           func(Reply)

	activeSession int
	MsgCache      *ccache.Cache
}

// New will instantiate a new client with the specified login details without connecting.
func New(user, password, server, id, cmppVersion string, timeout time.Duration) *CMPPClient {

	cred := &Credentials{
		User:     user,
		Password: password,
		Server:   server,
	}
	cache := ccache.New(ccache.Configure())
	var clientVersion cmpp.Type
	switch cmppVersion {
	case "2.0":
		clientVersion = cmpp.V20
		break
	case "2.1":
		clientVersion = cmpp.V21
		break
	case "3.0":
		clientVersion = cmpp.V30
		break
	default:
		clientVersion = cmpp.V21
	}
	return &CMPPClient{
		ID:                id,
		Credentials:       cred,
		ClientVersion:     clientVersion,
		ConnectionTimeout: timeout,
		MessageChan:       make(chan *Message, 10000),
		MsgCache:          cache,
	}
}

func (m *CMPPClient) initClient(cmppVersion cmpp.Type, firstConnection bool, b *backoff.Backoff) error {
	m.Client = cmpp.NewClient(cmppVersion)
	return nil
}

func (m *CMPPClient) connect() {
	b := &backoff.Backoff{
		Min:    time.Second,
		Max:    1 * time.Minute,
		Jitter: true,
	}

	m.Connected = false

	// setup websocket connection
	url := m.Credentials.Server

	log.Infof("CMPPClient %s: making connection: %s", m.ID, url)
	for {
		c := m.Client
		cred := m.Credentials
		err := c.Connect(url, cred.User, cred.Password, m.ConnectionTimeout)
		if err != nil {
			d := b.Duration()
			log.With("ID", m.ID).Infof("CMPPClient %s: %s, reconnecting in %s", m.ID, err, d)
			time.Sleep(d)
			continue
		}
		break
	}

	log.With("ID", m.ID).Debugf("CMPPClient %s: connected", m.ID)
	m.PingChan = make(chan string, 20)
	m.ReconnectChan = make(chan int)
	// only start to parse CMPPmessages when login is completely done
	m.Connected = true
}

// Do nothing as the function is already implemented in the underlying cmpp library
func (m *CMPPClient) checkAlive() error {
	// check if session still is valid
	m.Client.SendReqPkt(&cmpp.CmppActiveTestReqPkt{})
	return nil
}

func digestString(s string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(s))) //nolint:gosec
}

// Login tries to connect the client with the details with which it was initialized.
func (m *CMPPClient) Login() error {
	// check if this is a first connect or a reconnection
	firstConnection := true
	if m.Connected {
		firstConnection = false
	}
	m.Connected = false
	if m.Quit {
		return nil
	}
	b := &backoff.Backoff{
		Min:    time.Second,
		Max:    5 * time.Minute,
		Jitter: true,
	}

	// do initialization setup
	if err := m.initClient(m.ClientVersion, firstConnection, b); err != nil {
		return err
	}

	m.connect()
	if m.activeSession < 0 {
		m.activeSession = -m.activeSession
	}
	m.activeSession++
	return nil
}

// Logout disconnects the client from the chat server.
func (m *CMPPClient) Logout() error {
	log.With("ID", m.ID).Debugf("CMPPClient %s, logout as %s on %s", m.ID, m.Credentials.User, m.Credentials.Server)
	m.Quit = true
	m.Client.Disconnect()
	if m.activeSession > 0 {
		m.activeSession = -m.activeSession
	}
	return nil
}

// TpUdhiSeq tpUdhiHeader 序列
var TpUdhiSeq byte = 0x00

func (m *CMPPClient) splitLongSms(content string) [][]byte {
	smsLength := 140
	smsHeaderLength := 6
	smsBodyLen := smsLength - smsHeaderLength
	contentBytes := []byte(content)
	chunks := [][]byte{}
	num := 1
	if (len(content)) > 140 {
		num = int(math.Ceil(float64(len(content)) / float64(smsBodyLen)))
	}
	if num == 1 {
		chunks = append(chunks, contentBytes)
		return chunks
	}
	tpUdhiHeader := []byte{0x05, 0x00, 0x03, TpUdhiSeq, byte(num)}
	TpUdhiSeq++

	for i := 0; i < num; i++ {
		chunk := tpUdhiHeader
		chunk = append(chunk, byte(i+1))
		smsBodyLen := smsLength - smsHeaderLength
		offset := i * smsBodyLen
		max := offset + smsBodyLen
		if max > len(content) {
			max = len(content)
		}

		chunk = append(chunk, contentBytes[offset:max]...)
		chunks = append(chunks, chunk)
	}
	return chunks
}

func (m *CMPPClient) messageToPackets(message *Message) []*cmpp.Cmpp2SubmitReqPkt {
	packets := make([]*cmpp.Cmpp2SubmitReqPkt, 0)
	// fixme:
	content, err := cmpputils.Utf8ToUcs2(message.Content)
	if err != nil {
		log.With("ID", m.ID).Error("messageToPackets error, ", err)
		return nil
	}
	chunks := m.splitLongSms(content)
	var tpUdhi uint8
	if len(chunks) > 1 {
		tpUdhi = 1
	}
	for i, chunk := range chunks {
		p := &cmpp.Cmpp2SubmitReqPkt{
			PkTotal:            uint8(len(chunks)),
			PkNumber:           uint8(i + 1),
			RegisteredDelivery: 1,
			MsgLevel:           1,
			ServiceId:          message.ServiceID,
			FeeUserType:        2,
			TpUdhi:             tpUdhi,
			FeeTerminalId:      message.Mobile,
			MsgFmt:             8,
			MsgSrc:             "900001",
			FeeType:            "02",
			FeeCode:            "10",
			// FIXME:
			ValidTime:      "151105131555101+",
			AtTime:         "",
			SrcId:          message.SourceID,
			DestUsrTl:      1,
			DestTerminalId: []string{message.Mobile},
			MsgLength:      uint8(len(chunk)),
			MsgContent:     string(chunk),
		}
		packets = append(packets, p)
	}

	return packets
}

// Receiver implements the core loop that manages the connection to the chat server. In
// case of a disconnect it will try to reconnect. A call to this method is blocking until
// the 'Quit' field of the CMPPClient object is set to 'true'.
func (m *CMPPClient) Receiver(id int) {
	for {
		var packet interface{}
		var err error

		if m.Quit || m.activeSession != id {
			log.With("ID", m.ID).Warn("Receiver exiting: ", id)
			return
		}

		if !m.Connected {
			time.Sleep(time.Millisecond * 100)
			continue
		}
		if packet, err = m.Client.RecvAndUnpackPkt(0); err != nil {
			log.With("ID", m.ID).Errorf("Receiver %d client read and unpack packet error: %v", id, err)
			m.ReconnectChan <- id
			time.Sleep(time.Second * 2)
			continue
		}

		if packet != nil {
			log.With("ID", m.ID).Debugf("Receiver received packet: %v", packet)
			if err = m.processPacket(packet); err != nil {
				log.With("ID", m.ID).Error("client processPacket error:", err)
			}
			continue
		}
	}
}

// Sender implements the core loop that sends the outbound message
func (m *CMPPClient) Sender(id int) {
	for {
		var err error
		if m.Quit || m.activeSession != id {
			log.With("ID", m.ID).Warn("Sender exiting: ", id)
			return
		}

		if !m.Connected {
			time.Sleep(time.Millisecond * 100)
			continue
		}
		select {
		case message := <-m.MessageChan:
			log.With("ID", m.ID).Debug("Sender dequeued a message: ", message)
			now := time.Now()
			if now.After(message.ValidBefore) {
				log.With("ID", m.ID).Warnf("outgoing message expired: message is valid before %v but now it's %v", message.ValidBefore, now)
				continue
			}
			packets := m.messageToPackets(message)
			for i, p := range packets {
				var seq uint32
				seq, err = m.Client.SendReqPkt(p)
				if err != nil {
					log.With("ID", m.ID).Errorf("Sender send cmpp2 submit request error: %s.", err)
					message.OnResult(err, 0, 0)
					break
				} else {
					log.With("ID", m.ID).Debugf("Sender send cmpp2 submit request ok, seq: %v", seq)
					if i == len(packets)-1 {
						m.MsgCache.Set(digestString(strconv.Itoa(int(seq))), message, time.Second*30)
					}
				}
			}
		case <-time.After(time.Second * 30):
			log.With("ID", m.ID).Debug("Sender, no messages from MessageChan in 30 seconds")
			continue
		}
	}
}

// SendMessage enqueue the message to the message channel
func (m *CMPPClient) SendMessage(mobile, content, sourceID, serviceID string, validBefore time.Time, callback func(error, uint64, uint8)) {
	if !m.Connected {
		log.With("ID", m.ID).Error("cmpp server not connected")
		go callback(errors.New("cmpp server not connected"), 0, 0)
	}
	message := Message{
		Mobile:      mobile,
		Content:     content,
		SourceID:    sourceID,
		ServiceID:   serviceID,
		ValidBefore: validBefore,
		OnResult: func(err error, msgId uint64, code uint8) {
			if callback != nil {
				callback(err, msgId, code)
			}
		},
	}
	select {
	case m.MessageChan <- &message:
		log.With("ID", m.ID).Debug("SendMessage, message enqueued")
	default:
		log.With("ID", m.ID).Error("SendMessage, MessageChan full")
		if callback != nil {
			go callback(errors.New("MessageChan full"), 0, 0)
		}
	}
}

func (m *CMPPClient) processPacket(packet interface{}) error {
	switch p := packet.(type) {
	case *cmpp.Cmpp2SubmitRspPkt:
		log.With("ID", m.ID).Debugf("processPacket: received a cmpp2 submit response: %v.", p)
		// 扣费
		// 回调
		seq := p.SeqId
		key := digestString(strconv.Itoa(int(seq)))
		item := m.MsgCache.Get(key)
		if item != nil {
			message := item.Value().(*Message)
			m.MsgCache.Delete(key)
			go message.OnResult(nil, p.MsgId, p.Result)
		}
	case *cmpp.Cmpp2DeliverReqPkt:
		log.With("ID", m.ID).Debugf("processPacket: received a cmpp2 delivery request: %v.", p)
		if p.RegisterDelivery == 1 {
			log.With("ID", m.ID).Debugf("processPacket, the cmpp2 delivery request: %d is a status report.", p.MsgId)
			log.With("ID", m.ID).Debug("processPacket: delivery request's message content is: ", p.MsgContent)
			contentBytes := []byte(p.MsgContent)
			if m.OnReport != nil {
				submitTime := ""
				if len(contentBytes) > 25 {
					submitTime = string(contentBytes[15:25])
					log.With("ID", m.ID).Debug("submitTime: ", submitTime)
				}
				doneTime := ""
				if len(contentBytes) > 35 {
					doneTime = string(contentBytes[25:35])
					log.With("ID", m.ID).Debug("doneTime: ", doneTime)
				}
				destTerminalID := ""
				if len(contentBytes) > 56 {
					destTerminalID = string(bytes.Trim(contentBytes[35:56], "\x00"))
					log.With("ID", m.ID).Debug("destTerminalID: ", destTerminalID)
				}
				seq := uint32(0)
				if len(contentBytes) > 60 {
					seq = binary.BigEndian.Uint32(contentBytes[56:60])
					log.With("ID", m.ID).Debug("seq: ", seq)
				}
				stat := string(bytes.Trim(contentBytes[8:15], "\x00"))
				log.With("ID", m.ID).Debug("stat: ", stat)
				go m.OnReport(DeliveryReport{
					MessageID:      binary.BigEndian.Uint64(contentBytes[0:8]),
					Status:         stat,
					SubmitTime:     submitTime,
					DoneTime:       doneTime,
					DestTerminalID: destTerminalID,
					SmscSequence:   seq,
				})
			}
		} else {
			log.With("ID", m.ID).Debugf("processPacket, the cmpp2 delivery request: %d is likely to be a reply.", p.MsgId)
			log.With("ID", m.ID).Debugf("processPacket: delivery request's message content is: %v", p.MsgContent)
			content := p.MsgContent
			var err error
			if p.MsgFmt == 8 {
				content, err = cmpputils.Ucs2ToUtf8(p.MsgContent)
				if err != nil {
					log.With("ID", m.ID).Error("Ucs2ToUtf8 error, ", err)
					return err
				}
				log.With("ID", m.ID).Debugf("processPacket: delivery request's message utf-8 decoded content is: %v", content)
			}
			if m.OnReply != nil {
				go m.OnReply(Reply{
					MessageID:     p.MsgId,
					Content:       content,
					SrcTerminalID: p.SrcTerminalId,
					DestID:        p.DestId,
				})
			}
		}
		rsp := &cmpp.Cmpp2DeliverRspPkt{
			MsgId:  p.MsgId,
			Result: 0,
		}
		err := m.Client.SendRspPkt(rsp, p.SeqId)
		if err != nil {
			log.With("ID", m.ID).Errorf("processPacket, error sending cmpp delivery response: %#v.", err)
			return err
		}
		log.With("ID", m.ID).Debug("processPacket, cmpp delivery response sent: ", rsp)

	case *cmpp.CmppActiveTestReqPkt:
		log.With("ID", m.ID).Debugf("processPacket, received a cmpp active request: %v.", p)
		rsp := &cmpp.CmppActiveTestRspPkt{}
		select {
		case m.PingChan <- "PING_FROM_SERVER":
		default:
		}
		err := m.Client.SendRspPkt(rsp, p.SeqId)
		if err != nil {
			log.With("ID", m.ID).Errorf("processPacket, send cmpp active response error: %#v", err)
			return err
		}
	case *cmpp.CmppActiveTestRspPkt:
		log.With("ID", m.ID).Debugf("processPacket, received a cmpp activetest response: %v.", p)
		select {
		case m.PingChan <- "PONG_FROM_SERVER":
		default:
		}
	case *cmpp.CmppTerminateReqPkt:
		log.With("ID", m.ID).Debugf("processPacket, received a cmpp termination request: %v.", p)
		rsp := &cmpp.CmppTerminateRspPkt{}
		err := m.Client.SendRspPkt(rsp, p.SeqId)
		if err != nil {
			log.With("ID", m.ID).Errorf("processPacket, error sending cmpp termination response: %#v.", err)
			return err
		}
		m.Client.Disconnect()
		// onTerminate(p)
	case *cmpp.CmppTerminateRspPkt:
		log.With("ID", m.ID).Debugf("processPacket, received a cmpp termination response: %v.", p)
	}
	return nil
}

// KeepAlive implements a ping-cycle that ensures that the connection to the chat servers
// remains alive.
func (m *CMPPClient) KeepAlive(id int) {
	retries := 0
	for {
		if m.Quit || m.activeSession != id {
			log.With("ID", m.ID).Warn("KeepAlive exiting: ", id)
			return
		}

		if m.Connected {
			if err := m.checkAlive(); err != nil {
				log.With("ID", m.ID).Warnf("KeepAlive, connection is not alive: %v", err)
			}
			select {
			case command := <-m.PingChan:
				log.With("ID", m.ID).Debug("received ", command)
				retries = 0
			case <-time.After(time.Second * 5):
				if retries < 3 {
					retries++
				} else {
					log.With("ID", m.ID).Warn("KeepAlive, timeout")
					m.ReconnectChan <- id
					// m.login(true)
				}
			}
		}
		time.Sleep(time.Second * 5)
		log.With("ID", m.ID).Debug("KeepAlive next loop...")
	}
}

func (m *CMPPClient) login(logoutFirst bool) error {
	if logoutFirst {
		m.Logout()
	}
	m.Quit = false
	err := m.Login()
	if err != nil {
		log.With("ID", m.ID).Errorf("Login failed: %#v", err)
		return err
	}

	go m.Receiver(m.activeSession)
	go m.Sender(m.activeSession)
	go m.KeepAlive(m.activeSession)
	if m.OnConnected != nil {
		m.OnConnected()
	}
	return nil
}

// StatusLoop implements a ping-cycle that ensures that the connection to the chat servers
// remains alive. In case of a disconnect it will try to reconnect. A call to this method
// is blocking until the 'Quit' field of the CMPPClient object is set to 'true'.
func (m *CMPPClient) StatusLoop() error {
	if err := m.login(false); err != nil {
		return err
	}
	log.With("ID", m.ID).Debug("StatusLoop:", m.OnConnected != nil)
	for {
		if m.Quit {
			return nil
		}
		select {
		case id := <-m.ReconnectChan:
			log.With("ID", m.ID).Warn("StatusLoop received signal from ReconnectChan: ", id)
			if m.activeSession == id {
				log.With("ID", m.ID).Warnf("StatusLoop activeSession is %d, logging in again...", id)
				m.login(true)
			}
		case <-time.After(time.Second * 10):
			continue
		}
	}
}

// Run starts a subroutine for the cmpp client's status loop
func (m *CMPPClient) Run() {
	go m.StatusLoop()
}
