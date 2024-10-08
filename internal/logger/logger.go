package logger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/0x4d31/galah/pkg/enrich"
	"github.com/0x4d31/galah/pkg/llm"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	errorInvalidJSONResponse = "invalidJSONResponse"
	errorEmptyLLMResponse    = "emptyLLMResponse"
	errorContentGeneration   = "contentGenerationError"
)

// New creates a new Logger instance with the specified configuration.
func New(eventLogFile string, modelConfig llm.Config, eCache *enrich.Enricher, sessionizer *Sessionizer, l *logrus.Logger) (*Logger, error) {
	eventLogger := logrus.New()
	eventLogger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime: "timestamp",
		},
	})
	evFile, err := os.OpenFile(eventLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	eventLogger.Out = evFile

	return &Logger{
		EnrichCache: eCache,
		Sessionizer: sessionizer,
		EventLogger: eventLogger,
		LLMConfig:   modelConfig,
		Logger:      l,
	}, nil
}

// LogError logs a failedResponse event.
func (l *Logger) LogError(r *http.Request, resp, port string, err error) {
	fields := l.commonFields(r, port)
	errorFields := errorFields(err, resp)
	for k, v := range errorFields {
		fields[k] = v
	}
	fields["response.metadata.provider"] = l.LLMConfig.Provider
	fields["response.metadata.model"] = l.LLMConfig.Model
	fields["response.metadata.temperature"] = l.LLMConfig.Temperature

	l.EventLogger.WithFields(fields).Error("failedResponse: returned 500 internal server error")
}

// LogEvent logs a successfulResponse event.
func (l *Logger) LogEvent(r *http.Request, resp llm.JSONResponse, port, respSource string) {
	fields := l.commonFields(r, port)

	// Flatten response headers
	for k, v := range resp.Headers {
		fields["response.headers."+k] = v
	}

	fields["response.body"] = resp.Body

	fields["response.metadata.generationSource"] = respSource
	fields["response.metadata.provider"] = l.LLMConfig.Provider
	fields["response.metadata.model"] = l.LLMConfig.Model
	fields["response.metadata.temperature"] = l.LLMConfig.Temperature

	l.EventLogger.WithFields(fields).Info("successfulResponse")
}

func (l *Logger) commonFields(r *http.Request, port string) logrus.Fields {
	now := time.Now()

	srcIP, srcPort, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		srcIP = r.RemoteAddr
		srcPort = ""
	}

	var tags []string
	var host string
	srcIPInfo, err := l.EnrichCache.Process(srcIP)
	if err != nil {
		l.Logger.Errorf("error getting enrichment info for %q: %s", srcIP, err)
	} else if srcIPInfo != nil {
		if s := srcIPInfo.KnownScanner; s != "" {
			tags = append(tags, s)
		}
		if h := srcIPInfo.Host; h != "" {
			host = h
		}
	}

	sensorName, err := getHostname()
	if err != nil {
		sensorName = uuid.NewString()
	}

	headerKeys := headerKeys(r.Header)
	sort.Strings(headerKeys)
	bodyBytes, _ := io.ReadAll(r.Body)

	sessionID, err := l.Sessionizer.Process(srcIP, now)
	if err != nil {
		l.Logger.Errorf("error generating session ID for %q: %s", srcIP, err)
	}

	// Flatten all the fields directly, rather than nesting them
	fields := logrus.Fields{
		//	"timestamp":          now,
		"src_ip":             srcIP,
		"hostname":           host,
		"src_port":           srcPort,
		"tags":               strings.Join(tags, ","),
		"sensorName":         sensorName,
		"dest_port":          port,
		"session":            sessionID,
		"request.method":     r.Method,
		"request.protocol":   r.Proto,
		"request.requestURI": r.RequestURI,
		"request.userAgent":  r.UserAgent(),
		"request.body":       string(bodyBytes),
		"request.bodySha256": func(data []byte) string {
			hash := sha256.Sum256(data)
			return hex.EncodeToString(hash[:])
		}(bodyBytes),
		"request.headers.sorted":       strings.Join(headerKeys, ","),
		"request.headers.sortedSha256": headersSortedSha256(headerKeys),
	}

	// Flatten headers
	for k, v := range convertMap(r.Header) {
		fields["request.headers."+k] = v
	}

	return fields
}

func errorFields(err error, resp string) logrus.Fields {
	errMsg := err.Error()
	var errorType string

	switch {
	case strings.Contains(errMsg, errorInvalidJSONResponse):
		errorType = errorInvalidJSONResponse
		errMsg = strings.ReplaceAll(errMsg, errorInvalidJSONResponse+": ", "")
	case strings.Contains(errMsg, errorEmptyLLMResponse):
		errorType = errorEmptyLLMResponse
		errMsg = strings.ReplaceAll(errMsg, errorEmptyLLMResponse+": ", "")
	default:
		errorType = errorContentGeneration
		errMsg = strings.ReplaceAll(errMsg, errorContentGeneration+": ", "")
	}

	return logrus.Fields{
		"type":            errorType,
		"msg":             errMsg,
		"invalidResponse": resp,
	}
}

func getHostname() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %s", err)
	}
	return hostname, nil
}

func headerKeys(headers http.Header) []string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	return keys
}

func headersSortedSha256(headerKeys []string) string {
	hash := sha256.Sum256([]byte(strings.Join(headerKeys, ",")))
	return hex.EncodeToString(hash[:])
}

func convertMap(input map[string][]string) map[string]string {
	result := make(map[string]string)

	for key, values := range input {
		result[key] = strings.Join(values, ", ")
	}

	return result
}
