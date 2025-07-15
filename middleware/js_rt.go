package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"one-api/common"
	"one-api/model"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type JSRuntime struct {
	vm *goja.Runtime
	mu sync.RWMutex
}

type JSContext struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Body        any               `json:"body"`
	Query       map[string]string `json:"query"`
	Params      map[string]string `json:"params"`
	UserAgent   string            `json:"userAgent"`
	RemoteIP    string            `json:"remoteIP"`
	ContentType string            `json:"contentType"`
	Extra       map[string]any    `json:"extra"`
}

func parseBodyByType(bodyBytes []byte, contentType string) any {
	if len(bodyBytes) == 0 {
		return ""
	}

	bodyStr := string(bodyBytes)

	// 根据 Content-Type 判断
	switch {
	case strings.Contains(contentType, "application/json"):
		var jsonObj any
		if err := json.Unmarshal(bodyBytes, &jsonObj); err == nil {
			return jsonObj
		}
		return bodyStr // JSON 解析失败时返回字符串

	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		// 解析为 map[string]string
		values, err := url.ParseQuery(bodyStr)
		if err == nil {
			result := make(map[string]string)
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}
		return bodyStr

	case strings.Contains(contentType, "multipart/form-data"):
		// multipart 数据保持为字节数组，JS 中需要特殊处理
		return bodyBytes

	case strings.Contains(contentType, "text/"):
		// 文本类型返回字符串
		return bodyStr

	default:
		// 尝试 JSON 解析
		var jsonObj any
		if err := json.Unmarshal(bodyBytes, &jsonObj); err == nil {
			return jsonObj
		}

		// 检查是否是 URL encoded
		if values, err := url.ParseQuery(bodyStr); err == nil && len(values) > 0 {
			result := make(map[string]string)
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}

		// 二进制数据或未知格式
		if isBinary(bodyBytes) {
			return bodyBytes
		}

		return bodyStr
	}
}

// 检查是否为二进制数据
func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	// 检查前 512 字节中是否包含控制字符（除了常见的换行符等）
	checkLen := min(len(data), 512)

	for i := range checkLen {
		b := data[i]
		// 控制字符但不是常见的文本字符
		if b < 32 && b != 9 && b != 10 && b != 13 {
			return true
		}
		// 非 UTF-8 字符
		if b > 127 {
			return true
		}
	}
	return false
}

func createJSContext(c *gin.Context) *JSContext {
	var bodyBytes []byte
	if c.Request != nil && c.Request.Body != nil {
		bodyBytes, _ = io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	// headers map
	headers := make(map[string]string)
	if c.Request != nil && c.Request.Header != nil {
		for key, values := range c.Request.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
	}

	// query parameters map
	query := make(map[string]string)
	if c.Request != nil && c.Request.URL != nil {
		for key, values := range c.Request.URL.Query() {
			if len(values) > 0 {
				query[key] = values[0]
			}
		}
	}

	// path parameters map
	params := make(map[string]string)
	for _, param := range c.Params {
		params[param.Key] = param.Value
	}

	method := ""
	url := ""
	userAgent := ""
	remoteIP := ""
	contentType := ""

	if c.Request != nil {
		method = c.Request.Method
		if c.Request.URL != nil {
			url = c.Request.URL.String()
		}
		userAgent = c.Request.UserAgent()
		contentType = c.ContentType()
	}

	if c != nil {
		remoteIP = c.ClientIP()
	}

	// 智能解析 body
	parsedBody := parseBodyByType(bodyBytes, contentType)

	return &JSContext{
		Method:      method,
		URL:         url,
		Headers:     headers,
		Body:        parsedBody,
		Query:       query,
		Params:      params,
		UserAgent:   userAgent,
		RemoteIP:    remoteIP,
		ContentType: contentType,
		Extra:       make(map[string]any),
	}
}

type JSResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

type JSDatabase struct {
	db *gorm.DB
}

var (
	jsRuntime     *JSRuntime
	jsRuntimeOnce sync.Once
)

func initJSRuntime() *JSRuntime {
	jsRuntimeOnce.Do(func() {
		jsRuntime = &JSRuntime{
			vm: goja.New(),
		}
		jsRuntime.setupGlobals()
		jsRuntime.loadScripts()
		common.SysLog("JavaScript runtime initialized successfully")
	})
	return jsRuntime
}

func (js *JSRuntime) setupGlobals() {
	js.mu.Lock()
	defer js.mu.Unlock()

	// console
	console := js.vm.NewObject()
	console.Set("log", func(args ...any) {
		var strs []string
		for _, arg := range args {
			strs = append(strs, fmt.Sprintf("%v", arg))
		}
		common.SysLog("JS: " + strings.Join(strs, " "))
	})
	console.Set("error", func(args ...any) {
		var strs []string
		for _, arg := range args {
			strs = append(strs, fmt.Sprintf("%v", arg))
		}
		common.SysError("JS: " + strings.Join(strs, " "))
	})
	js.vm.Set("console", console)

	// JSON
	jsonObj := js.vm.NewObject()
	jsonObj.Set("parse", func(str string) any {
		var result any
		err := json.Unmarshal([]byte(str), &result)
		if err != nil {
			panic(js.vm.ToValue(err.Error()))
		}
		return result
	})
	jsonObj.Set("stringify", func(obj any) string {
		data, err := json.Marshal(obj)
		if err != nil {
			panic(js.vm.ToValue(err.Error()))
		}
		return string(data)
	})
	js.vm.Set("JSON", jsonObj)

	js.vm.Set("db", &JSDatabase{db: model.DB})
}

func (jsdb *JSDatabase) Query(sql string, args ...any) []map[string]any {
	rows, err := jsdb.db.Raw(sql, args...).Rows()
	if err != nil {
		common.SysError("JS DB Query Error: " + err.Error())
		return nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		common.SysError("JS DB Columns Error: " + err.Error())
		return nil
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			common.SysError("JS DB Scan Error: " + err.Error())
			continue
		}

		row := make(map[string]any)
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	return results
}

func (jsdb *JSDatabase) Exec(sql string, args ...any) map[string]any {
	result := jsdb.db.Exec(sql, args...)
	return map[string]any{
		"rowsAffected": result.RowsAffected,
		"error":        result.Error,
	}
}

func (js *JSRuntime) loadScripts() {
	// 加载预处理脚本
	if preScript, err := os.ReadFile("scripts/pre_process.js"); err == nil {
		js.mu.Lock()
		_, err = js.vm.RunString(string(preScript))
		js.mu.Unlock()
		if err != nil {
			common.SysError("Failed to load pre_process.js: " + err.Error())
		} else {
			common.SysLog("Loaded pre_process.js")
		}
	}

	// 加载后处理脚本
	if postScript, err := os.ReadFile("scripts/post_process.js"); err == nil {
		js.mu.Lock()
		_, err = js.vm.RunString(string(postScript))
		js.mu.Unlock()
		if err != nil {
			common.SysError("Failed to load post_process.js: " + err.Error())
		} else {
			common.SysLog("Loaded post_process.js")
		}
	}
}

func (js *JSRuntime) ReloadScripts() {
	js.loadScripts()
}

// validateGinContext checks if the gin context is properly initialized
func validateGinContext(c *gin.Context) error {
	if c == nil {
		return fmt.Errorf("gin context is nil")
	}
	if c.Request == nil {
		return fmt.Errorf("gin context request is nil")
	}
	return nil
}

func (js *JSRuntime) PreProcessRequest(c *gin.Context) error {
	if err := validateGinContext(c); err != nil {
		common.SysError("JS PreProcess Validation Error: " + err.Error())
		return err
	}

	js.mu.RLock()
	preProcessFunc := js.vm.Get("preProcessRequest")
	js.mu.RUnlock()

	if preProcessFunc == nil || goja.IsUndefined(preProcessFunc) {
		return nil // 没有预处理函数
	}

	jsCtx := createJSContext(c)
	if jsCtx == nil {
		return fmt.Errorf("failed to create JS context")
	}

	js.mu.Lock()
	defer js.mu.Unlock()

	js.vm.Set("ctx", jsCtx)
	fn, ok := goja.AssertFunction(preProcessFunc)
	if !ok {
		return fmt.Errorf("preProcessRequest is not a function")
	}

	result, err := fn(goja.Undefined(), js.vm.ToValue(jsCtx))

	if err != nil {
		common.SysError("JS PreProcess Error: " + err.Error())
		return err
	}

	// 处理返回结果
	if result != nil && !goja.IsUndefined(result) {
		resultObj := result.Export()
		if resultMap, ok := resultObj.(map[string]any); ok {
			// 是否修改请求
			if newBody, exists := resultMap["body"]; exists {
				switch v := newBody.(type) {
				case string:
					c.Request.Body = io.NopCloser(strings.NewReader(v))
					c.Request.ContentLength = int64(len(v))
				case []byte:
					c.Request.Body = io.NopCloser(bytes.NewBuffer(v))
					c.Request.ContentLength = int64(len(v))
				case map[string]any:
					bodyBytes, err := json.Marshal(v)
					if err == nil {
						c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
						c.Request.ContentLength = int64(len(bodyBytes))
						c.Request.Header.Set("Content-Type", "application/json")
					} else {
						common.SysError("JS PreProcess JSON Marshal Error: " + err.Error())
					}

				default:
					common.SysError("JS PreProcess Unsupported Body Type: " + fmt.Sprintf("%T", newBody))
				}
			}

			// 是否修改 headers
			if newHeaders, exists := resultMap["headers"]; exists {
				if headersMap, ok := newHeaders.(map[string]any); ok {
					for key, value := range headersMap {
						if valueStr, ok := value.(string); ok {
							c.Request.Header.Set(key, valueStr)
						}
					}
				}
			}

			// 是否阻止请求
			if block, exists := resultMap["block"]; exists {
				if blockBool, ok := block.(bool); ok && blockBool {
					status := http.StatusForbidden
					if statusCode, exists := resultMap["statusCode"]; exists {
						if statusInt, ok := statusCode.(float64); ok {
							status = int(statusInt)
						}
					}

					message := "Request blocked by pre-process script"
					if msg, exists := resultMap["message"]; exists {
						if msgStr, ok := msg.(string); ok {
							message = msgStr
						}
					}

					c.JSON(status, gin.H{"error": message})
					c.Abort()
					return fmt.Errorf("request blocked")
				}
			}
		}
	}

	return nil
}

func (js *JSRuntime) PostProcessResponse(c *gin.Context, statusCode int, body []byte) (int, []byte, error) {
	if err := validateGinContext(c); err != nil {
		common.SysError("JS PostProcess Validation Error: " + err.Error())
		return statusCode, body, err
	}

	js.mu.RLock()
	postProcessFunc := js.vm.Get("postProcessResponse")
	js.mu.RUnlock()

	if postProcessFunc == nil || goja.IsUndefined(postProcessFunc) {
		return statusCode, body, nil // 没有后处理
	}

	jsCtx := createJSContext(c)
	if jsCtx == nil {
		return statusCode, body, fmt.Errorf("failed to create JS context")
	}

	jsResponse := &JSResponse{
		StatusCode: statusCode,
		Headers:    make(map[string]string),
		Body:       string(body),
	}

	// 获取响应头
	if c.Writer != nil {
		for key, values := range c.Writer.Header() {
			if len(values) > 0 {
				jsResponse.Headers[key] = values[0]
			}
		}
	}

	js.mu.Lock()
	defer js.mu.Unlock()

	js.vm.Set("ctx", jsCtx)
	fn, ok := goja.AssertFunction(postProcessFunc)
	if !ok {
		return statusCode, body, fmt.Errorf("postProcessResponse is not a function")
	}
	result, err := fn(goja.Undefined(), js.vm.ToValue(jsCtx), js.vm.ToValue(jsResponse))

	if err != nil {
		common.SysError("JS PostProcess Error: " + err.Error())
		return statusCode, body, err
	}

	// 处理返回
	if result != nil && !goja.IsUndefined(result) {
		resultObj := result.Export()
		if resultMap, ok := resultObj.(map[string]any); ok {
			if newStatusCode, exists := resultMap["statusCode"]; exists {
				if statusInt, ok := newStatusCode.(float64); ok {
					statusCode = int(statusInt)
				}
			}

			if newBody, exists := resultMap["body"]; exists {
				if bodyStr, ok := newBody.(string); ok {
					body = []byte(bodyStr)
				}
			}

			if newHeaders, exists := resultMap["headers"]; exists {
				if headersMap, ok := newHeaders.(map[string]any); ok {
					for key, value := range headersMap {
						if valueStr, ok := value.(string); ok {
							c.Header(key, valueStr)
						}
					}
				}
			}
		}
	}

	return statusCode, body, nil
}

func JSRuntimeMiddleware() gin.HandlerFunc {
	if os.Getenv("JS_RUNTIME_ENABLED") != "true" {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	runtime := initJSRuntime()
	return func(c *gin.Context) {
		start := time.Now()

		// 预处理
		common.SysLog("JS Runtime PreProcessing Request: " + c.Request.Method + " " + c.Request.URL.String())
		if err := runtime.PreProcessRequest(c); err != nil {
			// 如果预处理返回错误，说明请求被阻止
			common.SysError("JS Runtime PreProcess Error: " + err.Error())
			return
		}
		common.SysLog("JS Runtime PreProcessing Completed")

		// 后处理
		if runtime.hasPostProcessFunction() {
			common.SysLog("JS Runtime PostProcessing Response")
			writer := &responseWriter{
				ResponseWriter: c.Writer,
				body:           &bytes.Buffer{},
				statusCode:     200, // 默认状态码
				headers:        make(map[string]string),
			}
			c.Writer = writer

			c.Next()

			// 后处理响应
			if writer.body.Len() > 0 {
				statusCode, body, err := runtime.PostProcessResponse(c, writer.statusCode, writer.body.Bytes())
				if err == nil {
					// 更新响应
					c.Writer = writer.ResponseWriter
					// Clear any existing content-length header to let Gin handle it
					c.Writer.Header().Del("Content-Length")
					c.Status(statusCode)
					c.Writer.Write(body)
					common.SysLog(fmt.Sprintf("JS Runtime PostProcessing Completed with status %d", statusCode))
				} else {
					// 出错时返回原始响应
					c.Writer = writer.ResponseWriter
					// Clear any existing content-length header to let Gin handle it
					c.Writer.Header().Del("Content-Length")
					c.Status(writer.statusCode)
					c.Writer.Write(writer.body.Bytes())
					common.SysError(fmt.Sprintf("JS Runtime PostProcess Error: %v", err))
				}
			} else {
				// 没有响应体时，恢复原始writer
				c.Writer = writer.ResponseWriter
				common.SysLog("JS Runtime PostProcessing Completed with no body")
			}
		} else {
			c.Next()
			common.SysLog("JS Runtime PostProcessing Skipped: No postProcessResponse function defined")
		}

		// 记录处理时间
		duration := time.Since(start)
		if duration > time.Millisecond*100 {
			common.SysLog(fmt.Sprintf("JS Runtime processing took %v", duration))
		}
	}
}

func (js *JSRuntime) hasPostProcessFunction() bool {
	js.mu.RLock()
	defer js.mu.RUnlock()
	postProcessFunc := js.vm.Get("postProcessResponse")
	return postProcessFunc != nil && !goja.IsUndefined(postProcessFunc)
}

type responseWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	written    bool
	headers    map[string]string
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if !w.written {
		w.statusCode = 200
		w.written = true
	}
	w.body.Write(data)
	return len(data), nil
}

func (w *responseWriter) WriteString(s string) (int, error) {
	if !w.written {
		w.statusCode = 200
		w.written = true
	}
	w.body.WriteString(s)
	return len(s), nil
}

func (w *responseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.written = true
	// 不立即调用原始的 WriteHeader，等后处理完成后再调用
}

func (w *responseWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func ReloadJSScripts() {
	if jsRuntime != nil {
		jsRuntime.ReloadScripts()
		common.SysLog("JavaScript scripts reloaded")
	}
}
