package main

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/labstack/echo/v4"
	"github.com/quiq/registry-ui/registry"
	"github.com/sirupsen/logrus"
)

// loggingMiddleware logging of the web framework
func loggingMiddleware() echo.MiddlewareFunc {
	logger := registry.SetupLogging("echo")
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ctx echo.Context) (err error) {
			req := ctx.Request()

			// Skip logging for specific paths.
			if strings.HasSuffix(req.RequestURI, "/event-receiver") {
				return next(ctx)
			}

			// Log the original request in DEBUG mode.
			if logrus.GetLevel() == logrus.DebugLevel && req.Body != nil {
				bodyBytes, _ := io.ReadAll(req.Body)
				req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
				if len(bodyBytes) > 0 {
					logger.Debugf("Incoming HTTP %s request: %s", req.Method, string(bodyBytes))
				}
			}

			res := ctx.Response()
			start := time.Now()
			if err = next(ctx); err != nil {
				ctx.Error(err)
			}
			stop := time.Now()

			statusCode := color.GreenString("%d", res.Status)
			switch {
			case res.Status >= 500:
				statusCode = color.RedString("%d", res.Status)
			case res.Status >= 400:
				statusCode = color.YellowString("%d", res.Status)
			case res.Status >= 300:
				statusCode = color.CyanString("%d", res.Status)
			}

			latency := stop.Sub(start).Round(1 * time.Millisecond).String() // human readable
			// latency := strconv.FormatInt(int64(stop.Sub(start)), 10) // in ns
			// Do main logging.
			logger.Infof("%s %s %s %s %s %s", ctx.RealIP(), req.Method, req.RequestURI, statusCode, latency, req.UserAgent())
			return
		}
	}
}

// recoverMiddleware recover from panics
func recoverMiddleware() echo.MiddlewareFunc {
	logger := registry.SetupLogging("echo")
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ctx echo.Context) error {
			defer func() {
				if r := recover(); r != nil {
					err, ok := r.(error)
					if !ok {
						err = fmt.Errorf("%v", r)
					}
					stackSize := 4 << 10 // 4 KB
					stack := make([]byte, stackSize)
					length := runtime.Stack(stack, true)
					logger.Errorf("[PANIC RECOVER] %v %s\n", err, stack[:length])
				}
			}()
			return next(ctx)
		}
	}
}
