package usage

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yuin/goldmark"
)

const Path = "/usage"

const queryTimeout = 30 * time.Second

const pageTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Coding Plan 用量汇总</title>
  <style>
    :root {
      color-scheme: light dark;
      --background: #f5f7fb;
      --surface: #ffffff;
      --text: #172033;
      --muted: #667085;
      --border: #e3e8f0;
      --accent: #2563eb;
      --quote: #eff6ff;
      --shadow: 0 12px 32px rgba(15, 23, 42, .08);
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --background: #0d1117;
        --surface: #161b22;
        --text: #e6edf3;
        --muted: #9da7b3;
        --border: #30363d;
        --accent: #79c0ff;
        --quote: #111d2e;
        --shadow: none;
      }
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--background);
      color: var(--text);
      font: 16px/1.7 -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    }
    main {
      width: min(860px, calc(100% - 32px));
      margin: 32px auto;
      padding: 28px 32px;
      border: 1px solid var(--border);
      border-radius: 16px;
      background: var(--surface);
      box-shadow: var(--shadow);
    }
    nav {
      display: flex;
      justify-content: flex-end;
      gap: 10px;
      margin-bottom: 12px;
    }
    nav a {
      padding: 5px 11px;
      border: 1px solid var(--border);
      border-radius: 8px;
      color: var(--accent);
      text-decoration: none;
      font-size: 14px;
    }
    nav a:hover { background: var(--quote); }
    h1 { margin: 0 0 6px; font-size: clamp(26px, 5vw, 36px); line-height: 1.25; }
    h2 { margin: 28px 0 10px; font-size: 20px; }
    blockquote {
      margin: 8px 0 22px;
      padding: 9px 14px;
      border-left: 4px solid var(--accent);
      border-radius: 0 8px 8px 0;
      background: var(--quote);
      color: var(--muted);
    }
    blockquote p { margin: 0; }
    ul { margin: 10px 0; padding-left: 24px; }
    li { margin: 8px 0; }
    hr { margin: 30px 0; border: 0; border-top: 1px solid var(--border); }
    strong { font-weight: 700; }
    @media (max-width: 600px) {
      main { width: 100%; margin: 0; padding: 20px; border: 0; border-radius: 0; box-shadow: none; }
    }
  </style>
</head>
<body>
  <main>
    <nav aria-label="页面操作">
      <a href="/usage">刷新</a>
      <a href="/usage?format=markdown">查看 Markdown</a>
    </nav>
    <article>{{.Content}}</article>
  </main>
</body>
</html>
`

var parsedPageTemplate = template.Must(template.New("usage").Parse(pageTemplate))

type Query func(ctx context.Context) (string, error)

type Handler struct {
	query  Query
	logger *slog.Logger
}

type templateData struct {
	Content template.HTML
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
	setSecurityHeaders(writer.Header())
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	format := request.URL.Query().Get("format")
	if format != "" && format != "html" && format != "markdown" {
		http.Error(writer, "format must be html or markdown", http.StatusBadRequest)
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

	if format == "markdown" {
		writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(content + "\n"))
		return
	}

	page, err := renderHTML(content)
	if err != nil {
		handler.logger.Error("渲染 HTTP 用量页面失败", "error", err)
		http.Error(writer, "failed to render usage", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(page)
}

func renderHTML(markdown string) ([]byte, error) {
	var content bytes.Buffer
	if err := goldmark.New().Convert([]byte(markdown), &content); err != nil {
		return nil, err
	}
	var page bytes.Buffer
	if err := parsedPageTemplate.Execute(&page, templateData{Content: template.HTML(content.String())}); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
