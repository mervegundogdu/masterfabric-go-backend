package llm

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/masterfabric-go/masterfabric/internal/shared/response"
)

type Handler struct {
	ollamaURL  string
	httpClient *http.Client
	logger     *slog.Logger
}

func NewHandler(ollamaURL string, logger *slog.Logger) *Handler {
	return &Handler{
		ollamaURL: strings.TrimRight(ollamaURL, "/"),
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		logger: logger,
	}
}

type ChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.JSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return
	}
	defer r.Body.Close()

	var chatReq ChatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		response.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if chatReq.Model == "" {
		chatReq.Model = "gemma:2b"
	}

	rewritten, err := json.Marshal(chatReq)
	if err != nil {
		response.JSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to marshal request"})
		return
	}

	targetURL := h.ollamaURL + "/v1/chat/completions"

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, strings.NewReader(string(rewritten)))
	if err != nil {
		response.JSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create proxy request"})
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	h.logger.Info("proxying LLM request", "model", chatReq.Model, "stream", chatReq.Stream)

	resp, err := h.httpClient.Do(proxyReq)
	if err != nil {
		h.logger.Error("ollama request failed", "error", err)
		response.JSON(w, http.StatusBadGateway, map[string]string{"error": "ollama server unavailable"})
		return
	}
	defer resp.Body.Close()

	if chatReq.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				_, writeErr := w.Write(buf[:n])
				if writeErr != nil {
					break
				}
				flusher.Flush()
			}
			if readErr != nil {
				break
			}
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			h.logger.Error("failed to copy response body", "error", err)
		}
	}
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	resp, err := h.httpClient.Get(h.ollamaURL + "/api/tags")
	if err != nil {
		h.logger.Error("ollama models request failed", "error", err)
		response.JSON(w, http.StatusBadGateway, map[string]string{"error": "ollama server unavailable"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Error("failed to copy models response", "error", err)
	}
}
