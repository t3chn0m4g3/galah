package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

var ignoreHeaders = map[string]bool{
	// Standard headers to ignore
	"content-length": true,
	"content-type":   true,
	"date":           true,
	"expires":        true,
	"last-modified":  true,
	// OpenAI made-up headers to ignore
	"http":     true,
	"http/1.0": true,
	"http/1.1": true,
	"http/1.2": true,
	"http/2.0": true,
}

func (app *App) startServers() error {
	var g errgroup.Group
	app.Servers = make(map[uint16]*http.Server)

	mu := sync.Mutex{}

	for _, pc := range app.Config.Ports {
		pc := pc // Capture the loop variable
		g.Go(func() error {
			server := app.setupServer(pc)

			var err error
			switch pc.Protocol {
			case "TLS":
				err = app.startTLSServer(server, pc)
			case "HTTP":
				err = app.startHTTPServer(server, pc)
			default:
				err = fmt.Errorf("unknown protocol for port %d", pc.Port)
			}

			if err != nil {
				logger.Errorf("error starting server on port %d: %s", pc.Port, err)
				return err
			}

			mu.Lock()
			app.Servers[pc.Port] = server
			mu.Unlock()

			return nil
		})
	}

	return g.Wait()
}

func (app *App) setupServer(pc PortConfig) *http.Server {
	serverAddr := fmt.Sprintf(":%d", pc.Port)
	server := &http.Server{
		Addr: serverAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			app.handleRequest(w, r, serverAddr)
		}),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return server
}

func (app *App) startTLSServer(server *http.Server, pc PortConfig) error {
	if pc.TLSProfile == "" {
		return fmt.Errorf("TLS profile is not configured for port %d", pc.Port)
	}

	tlsConfig, ok := app.Config.TLS[pc.TLSProfile]
	if !ok || tlsConfig.Certificate == "" || tlsConfig.Key == "" {
		return fmt.Errorf("TLS profile is incomplete for port %d", pc.Port)
	}

	logger.Infof("starting HTTPS server on port %d with TLS profile: %s", pc.Port, pc.TLSProfile)
	err := server.ListenAndServeTLS(tlsConfig.Certificate, tlsConfig.Key)
	if err != nil {
		return err
	}
	return nil
}

func (app *App) startHTTPServer(server *http.Server, pc PortConfig) error {
	logger.Infof("starting HTTP server on port %d", pc.Port)
	err := server.ListenAndServe()
	if err != nil {
		return err
	}
	return nil
}

func (app *App) handleRequest(w http.ResponseWriter, r *http.Request, serverAddr string) {
	_, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		port = ""
	}

	logger.Infof("port %s received a request for %q, from source %s", port, r.URL.String(), r.RemoteAddr)
	// Check the response cache
	response, err := app.checkCache(r, port)
	if err != nil {
		logger.Infof("request cache miss for %q: %s", r.URL.String(), err)
		// Call the LLM API to generate response
		responseString, err := app.generateLLMResponse(r)
		if err != nil {
			logger.Errorf("error generating response: %s", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		logger.Infof("generated HTTP response: %s", responseString)

		// Store the generated response in the cache
		response = []byte(responseString)
		key := getCacheKey(r, port)
		err = app.storeResponse(key, response)
	}

	// Parse the JSON-encoded data into a HTTPResponse struct, and send it to the client.
	var respData HTTPResponse
	if err := json.Unmarshal(response, &respData); err != nil {
		logger.Errorf("error unmarshalling the json-encoded data: %s", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	sendResponse(w, respData)
	logger.Infof("sent the generated response to %s", r.RemoteAddr)

	// The response headers are logged exactly as generated by OpenAI, however,
	// certain headers are excluded before sending the response to the client.
	event := app.makeEvent(r, respData, port)
	app.writeLog(event)
}

func sendResponse(w http.ResponseWriter, response HTTPResponse) {
	for key, value := range response.Headers {
		if !isExcludedHeader(key) {
			w.Header().Set(key, value)
		}
	}

	_, err := w.Write([]byte(response.Body))
	if err != nil {
		logger.Errorf("error writing response: %s", err)
	}
}

func isExcludedHeader(headerKey string) bool {
	return ignoreHeaders[strings.ToLower(headerKey)]
}

func (app *App) listenForShutdownSignals(ctx context.Context) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sig
		logger.Infof("received shutdown signal. shutting down servers...")

		for _, server := range app.Servers {
			if err := server.Shutdown(ctx); err != nil {
				logger.Errorf("error shutting down server: %s", err)
			}
		}

		logger.Infoln("all servers shut down gracefully.")
		os.Exit(0)
	}()
}
