package usage

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const Path = "/usage"

const queryTimeout = 30 * time.Second

type Query func(ctx context.Context) (string, error)

type Handler struct {
	query  Query
	logger *slog.Logger
}

func NewHandler(query Query, logger *slog.Logger) (*Handler, error) {
	if query == nil {
		return nil, errors.New("用量查询函数不能为空")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{query: query, logger: logger}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), queryTimeout)
	defer cancel()
	content, err := handler.query(ctx)
	if err != nil {
		handler.logger.Error("HTTP 用量查询失败", "error", err)
		http.Error(writer, "failed to query usage", http.StatusBadGateway)
		return
	}
	content = strings.TrimSpace(content)
	if content == "" {
		content = "# Coding Plan 用量汇总\n> 暂无可展示的用量数据"
	}

	writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(content + "\n"))
}
