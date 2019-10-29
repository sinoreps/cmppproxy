package main

import (
	"errors"

	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/caarlos0/env"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/render"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sinoreps/cmppproxy/callback"
	"github.com/sinoreps/cmppproxy/log"
	"github.com/sinoreps/cmppproxy/proxy"
)

// Config contains configs for the app
type Config struct {
	AppName           string        `env:"APP_NAME" envDefault:"CMPPProxy"`
	CMPPAccount       string        `env:"CMPP_ACCOUNT"`
	CMPPPassword      string        `env:"CMPP_PASSWORD"`
	CMPPServerAddr    string        `env:"CMPP_SERVER_ADDR"`
	CMPPVersion       string        `env:"CMPP_VERSION" envDefault:"2.1"`
	CMPPSourceID      string        `env:"CMPP_SOURCE_ID"`
	CMPPServiceID     string        `env:"CMPP_SERVICE_ID"`
	Port              int           `env:"PORT" envDefault:"8080"`
	SendTimeout       time.Duration `env:"SEND_TIMEOUT"  envDefault:"30s"`
	ReportWorkerCount int           `env:"REPORT_WORKER_COUNT" envDefault:"20"`
	ReportCallbackURL string        `env:"REPORT_CALLBACK_URL"`
	ReplyWorkerCount  int           `env:"REPLY_WORKER_COUNT" envDefault:"5"`
	ReplyCallbackURL  string        `env:"REPLY_CALLBACK_URL"`
}

// AppConfig holds the app config
var AppConfig *Config

// CMPPProxy is the channel for sending sms to upstream
var CMPPProxy *proxy.Proxy

// SendRequest data model
type SendRequest struct {
	Mobile  string `json:"mobile"`
	Content string `json:"content"`
	ExtNo   string `json:"extension_no"`
}

// SendResponse data model
type SendResponse struct {
	Mobile    string `json:"mobile"`
	MessageID string `json:"message_id"`
	Count     int    `json:"count"`
}

// GenericResponse is the renderer type for handling all sorts of errors.
type GenericResponse struct {
	Err            error `json:"-"` // low-level runtime error
	HTTPStatusCode int   `json:"-"` // http response status code

	StatusText string      `json:"status"`          // user-level status message
	AppCode    int64       `json:"code,omitempty"`  // application-specific error code
	Data       interface{} `json:"data,omitempty"`  // application-level data
	ErrorText  string      `json:"error,omitempty"` // application-level error message, for debugging
}

// Render is render method for GenericResponse
func (e *GenericResponse) Render(w http.ResponseWriter, r *http.Request) error {
	render.Status(r, e.HTTPStatusCode)
	return nil
}

// ErrInvalidRequest returns the "invalid request" renderer
func ErrInvalidRequest(err error) render.Renderer {
	return &GenericResponse{
		Err:            err,
		HTTPStatusCode: 400,
		StatusText:     "Invalid request.",
		ErrorText:      err.Error(),
	}
}

// func ErrRender(err error) render.Renderer {
// 	return &GenericResponse{
// 		Err:            err,
// 		HTTPStatusCode: 422,
// 		StatusText:     "Error rendering response.",
// 		ErrorText:      err.Error(),
// 	}
// }

// ErrSendFailed returns a renderer for app-level errors
func ErrSendFailed(err error) render.Renderer {
	return &GenericResponse{
		Err:            err,
		HTTPStatusCode: 200,
		AppCode:        -9000,
		StatusText:     "Send failed",
		ErrorText:      err.Error(),
	}
}

// SuccessRender returns a renderer for success repsonses
func SuccessRender(data interface{}) render.Renderer {
	return &GenericResponse{
		HTTPStatusCode: 200,
		AppCode:        1,
		Data:           data,
		StatusText:     "Success",
		ErrorText:      "",
	}
}

// Bind is the bind method for SendRequest to comply with the chi web framework
func (s *SendRequest) Bind(r *http.Request) error {
	if s.Mobile == "" {
		return errors.New("invalid mobile, mobile should not be empty")
	}
	if _, err := strconv.Atoi(s.Mobile); err != nil {
		return errors.New("invalid mobile, mobile should only contain digits")
	}
	if s.Content == "" {
		return errors.New("invalid content, content should not be empty")
	}
	if len(s.ExtNo) > 0 {
		if _, err := strconv.Atoi(s.ExtNo); err != nil {
			return errors.New("invalid ext_no, ext_no should only contain digits")
		}
	}
	return nil
}

// Send handles sms-sending request
func Send(w http.ResponseWriter, r *http.Request) {
	data := &SendRequest{}
	if err := render.Bind(r, data); err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	mobile := data.Mobile
	content := data.Content
	extNo := data.ExtNo

	// send
	count, messageID, err := CMPPProxy.Send(mobile, content, extNo, AppConfig.SendTimeout)
	if err != nil {
		render.Render(w, r, ErrSendFailed(err))
		return
	}

	responseData := SendResponse{
		Mobile:    mobile,
		MessageID: fmt.Sprintf("%v", messageID),
		Count:     count,
	}
	render.Render(w, r, SuccessRender(responseData))
	return
}

func main() {
	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		log.Errorf("%+v\n", err)
	}

	log.Infof("app config parsed from env variables, cfg: %+v", cfg)
	AppConfig = &cfg

	if len(cfg.CMPPServerAddr) < 1 {
		log.Errorf("invalid CMPPServerAddr: %v", cfg.CMPPServerAddr)
	}

	// Report callback workers
	for i := 0; i < cfg.ReportWorkerCount; i++ {
		go callback.SpawnReportWorker(i)
	}

	// Reply callback workers
	for i := 0; i < cfg.ReplyWorkerCount; i++ {
		go callback.SpawnReplyWorker(i)
	}

	CMPPProxy = proxy.New(
		cfg.AppName,
		cfg.CMPPServerAddr,
		cfg.CMPPAccount,
		cfg.CMPPPassword,
		cfg.CMPPSourceID,
		cfg.CMPPServiceID,
		cfg.CMPPVersion,
	)
	CMPPProxy.InitIfNeeded()

	r := chi.NewRouter()

	// middlewares
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Set a timeout value on the request context (ctx), that will signal
	// through ctx.Done() that the request has timed out and further
	// processing should be stopped.
	r.Use(middleware.Timeout(cfg.SendTimeout))

	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})

	r.Post("/send", Send)

	r.Handle("/metrics", promhttp.Handler())

	log.Infof("Server will listen on port %v", cfg.Port)

	http.ListenAndServe(":"+strconv.Itoa(cfg.Port), r)
}
