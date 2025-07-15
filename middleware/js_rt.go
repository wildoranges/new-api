package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"one-api/common"
	"one-api/model"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/// Runtime 配置
type JSRuntimeConfig struct {
	Enabled        bool          `json:"enabled"`
	MaxVMCount     int           `json:"max_vm_count"`
	ScriptTimeout  time.Duration `json:"script_timeout"`
	PreScriptPath  string        `json:"pre_script_path"`
	PostScriptPath string        `json:"post_script_path"`
}

/// 池化
type JSRuntimePool struct {
	pool       chan *goja.Runtime
	maxSize    int
	createFunc func() *goja.Runtime
	scripts    map[string]string
	mu         sync.RWMutex
}

/// 上下文
type JSContext struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	// 可能是string、[]byte、map等
	Body        any               `json:"body"`
	UserAgent   string            `json:"userAgent"`
	RemoteIP    string            `json:"remoteIP"`
	Extra       map[string]any    `json:"extra"`
}

type JSResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

type JSDatabase struct {
	db *gorm.DB
}

type responseWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	headerMap  http.Header
	written    bool
}

var (
	jsRuntimePool *JSRuntimePool
	jsPoolOnce    sync.Once
	jsConfig      = JSRuntimeConfig{}
)

const (
	defaultPreScriptPath  = "scripts/pre_process.js"
	defaultPostScriptPath = "scripts/post_process.js"
	defaultScriptTimeout  = 5 * time.Second
	defaultMaxVMCount     = 8
)

func init() {
	if enabled := os.Getenv("JS_RUNTIME_ENABLED"); enabled != "" {
		jsConfig.Enabled = enabled == "true"
	}

	if maxCount := os.Getenv("JS_MAX_VM_COUNT"); maxCount != "" {
		if count, err := strconv.Atoi(maxCount); err == nil && count > 0 {
			jsConfig.MaxVMCount = count
		}
	} else {
		jsConfig.MaxVMCount = defaultMaxVMCount
	}

	if timeout := os.Getenv("JS_SCRIPT_TIMEOUT"); timeout != "" {
		if t, err := time.ParseDuration(timeout + "s"); err == nil && t > 0 {
			jsConfig.ScriptTimeout = t
		}
	} else {
		jsConfig.ScriptTimeout = defaultScriptTimeout
	}

	jsConfig.PreScriptPath = os.Getenv("JS_PREPROCESS_SCRIPT_PATH")
	if jsConfig.PreScriptPath == "" {
		jsConfig.PreScriptPath = defaultPreScriptPath
	}

	jsConfig.PostScriptPath = os.Getenv("JS_POSTPROCESS_SCRIPT_PATH")
	if jsConfig.PostScriptPath == "" {
		jsConfig.PostScriptPath = defaultPostScriptPath
	}
}

func parseBodyByType(bodyBytes []byte, contentType string) any {
	if len(bodyBytes) == 0 {
		return ""
	}

	bodyStr := string(bodyBytes)
	contentLower := strings.ToLower(contentType)

	switch {
	case strings.Contains(contentLower, "application/json"):
		var jsonObj any
		if err := json.Unmarshal(bodyBytes, &jsonObj); err == nil {
			return jsonObj
		}
		return bodyStr

	case strings.Contains(contentLower, "application/x-www-form-urlencoded"):
		if values, err := url.ParseQuery(bodyStr); err == nil {
			result := make(map[string]string, len(values))
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}
		return bodyStr

	case strings.Contains(contentLower, "multipart/form-data"):
		return bodyBytes

	case strings.Contains(contentLower, "text/"):
		return bodyStr

	default:
		// 尝试JSON解析
		var jsonObj any
		if json.Unmarshal(bodyBytes, &jsonObj) == nil {
			return jsonObj
		}

		// 尝试form解析
		if values, err := url.ParseQuery(bodyStr); err == nil && len(values) > 0 {
			result := make(map[string]string, len(values))
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}

		return bodyStr
	}
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

	parsedBody := parseBodyByType(bodyBytes, contentType)

	return &JSContext{
		Method:      method,
		URL:         url,
		Headers:     headers,
		Body:        parsedBody,
		UserAgent:   userAgent,
		RemoteIP:    remoteIP,
		Extra:       make(map[string]any),
	}
}

func NewJSRuntimePool(maxSize int) *JSRuntimePool {
	pool := &JSRuntimePool{
		pool:    make(chan *goja.Runtime, maxSize),
		maxSize: maxSize,
		scripts: make(map[string]string),
	}

	pool.createFunc = func() *goja.Runtime {
		vm := goja.New()
		pool.setupGlobals(vm)
		pool.loadScripts(vm)
		return vm
	}

	// 预创建
	preCreate := min(maxSize/2, 4)
	for range preCreate {
		select {
		case pool.pool <- pool.createFunc():
		default:
		}
	}

	return pool
}

func (p *JSRuntimePool) Get() *goja.Runtime {
	select {
	case vm := <-p.pool:
		return vm
	default:
		return p.createFunc()
	}
}

func (p *JSRuntimePool) Put(vm *goja.Runtime) {
	select {
	case p.pool <- vm:
	default:
		// 池满，丢弃VM让GC回收
	}
}

func (p *JSRuntimePool) setupGlobals(vm *goja.Runtime) {
	// console
	console := vm.NewObject()
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
	vm.Set("console", console)

	// JSON
	jsonObj := vm.NewObject()
	jsonObj.Set("parse", func(str string) any {
		var result any
		err := json.Unmarshal([]byte(str), &result)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return result
	})
	jsonObj.Set("stringify", func(obj any) string {
		data, err := json.Marshal(obj)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return string(data)
	})
	vm.Set("JSON", jsonObj)

	vm.Set("db", &JSDatabase{db: model.DB})
}

func (p *JSRuntimePool) loadScripts(vm *goja.Runtime) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 加载预处理脚本
	if script, exists := p.scripts["pre"]; exists {
		if _, err := vm.RunString(script); err != nil {
			common.SysError("Failed to load pre_process.js: " + err.Error())
		}
	} else if preScript, err := os.ReadFile(jsConfig.PreScriptPath); err == nil {
		p.scripts["pre"] = string(preScript)
		if _, err = vm.RunString(string(preScript)); err != nil {
			common.SysError("Failed to load pre_process.js: " + err.Error())
		} else {
			common.SysLog("Loaded pre_process.js")
		}
	}

	// 加载后处理脚本
	if script, exists := p.scripts["post"]; exists {
		if _, err := vm.RunString(script); err != nil {
			common.SysError("Failed to load post_process.js: " + err.Error())
		}
	} else if postScript, err := os.ReadFile(jsConfig.PostScriptPath); err == nil {
		p.scripts["post"] = string(postScript)
		if _, err = vm.RunString(string(postScript)); err != nil {
			common.SysError("Failed to load post_process.js: " + err.Error())
		} else {
			common.SysLog("Loaded post_process.js")
		}
	}
}

func (p *JSRuntimePool) ReloadScripts() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 清空缓存的脚本
	p.scripts = make(map[string]string)

	// 清空VM池，强制重新创建
	for {
		select {
		case <-p.pool:
		default:
			goto done
		}
	}
done:
	common.SysLog("JavaScript scripts reloaded")
}

func (jsdb *JSDatabase) Query(sql string, args ...any) []map[string]any {
	if jsdb.db == nil {
		common.SysError("JS DB is nil")
		return nil
	}

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

	results := make([]map[string]any, 0, 100)
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

		row := make(map[string]any, len(columns))
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
	if jsdb.db == nil {
		return map[string]any{
			"rowsAffected": int64(0),
			"error":        "database is nil",
		}
	}

	result := jsdb.db.Exec(sql, args...)
	return map[string]any{
		"rowsAffected": result.RowsAffected,
		"error":        result.Error,
	}
}

func initJSRuntimePool() *JSRuntimePool {
	jsPoolOnce.Do(func() {
		jsRuntimePool = NewJSRuntimePool(jsConfig.MaxVMCount)
		common.SysLog("JavaScript runtime pool initialized successfully")
	})
	return jsRuntimePool
}

func validateGinContext(c *gin.Context) error {
	if c == nil {
		return fmt.Errorf("gin context is nil")
	}
	if c.Request == nil {
		return fmt.Errorf("gin context request is nil")
	}
	return nil
}

func (p *JSRuntimePool) PreProcessRequest(c *gin.Context) error {
	if err := validateGinContext(c); err != nil {
		common.SysError("JS PreProcess Validation Error: " + err.Error())
		return err
	}

	vm := p.Get()
	defer p.Put(vm)

	preProcessFunc := vm.Get("preProcessRequest")
	if preProcessFunc == nil || goja.IsUndefined(preProcessFunc) {
		return nil
	}

	jsCtx := createJSContext(c)
	if jsCtx == nil {
		return fmt.Errorf("failed to create JS context")
	}

	type jsResult struct {
		result goja.Value
		err    error
	}

	resultChan := make(chan jsResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultChan <- jsResult{err: fmt.Errorf("JS panic: %v", r)}
			}
		}()

		vm.Set("ctx", jsCtx)
		fn, ok := goja.AssertFunction(preProcessFunc)
		if !ok {
			resultChan <- jsResult{err: fmt.Errorf("preProcessRequest is not a function")}
			return
		}

		result, err := fn(goja.Undefined(), vm.ToValue(jsCtx))
		resultChan <- jsResult{result: result, err: err}
	}()

	var err error
	var result goja.Value
	select {
	case res := <-resultChan:
		result, err = res.result, res.err
	// 超时控制
	case <-time.After(jsConfig.ScriptTimeout):
		return fmt.Errorf("JS preProcess timeout after %v", jsConfig.ScriptTimeout)
	}

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

func (p *JSRuntimePool) PostProcessResponse(c *gin.Context, statusCode int, body []byte) (int, []byte, error) {
	if err := validateGinContext(c); err != nil {
		common.SysError("JS PostProcess Validation Error: " + err.Error())
		return statusCode, body, err
	}

	vm := p.Get()
	defer p.Put(vm)

	postProcessFunc := vm.Get("postProcessResponse")
	if postProcessFunc == nil || goja.IsUndefined(postProcessFunc) {
		return statusCode, body, nil
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

	type jsResult struct {
		result goja.Value
		err    error
	}

	resultChan := make(chan jsResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultChan <- jsResult{err: fmt.Errorf("JS panic: %v", r)}
			}
		}()

		vm.Set("ctx", jsCtx)
		fn, ok := goja.AssertFunction(postProcessFunc)
		if !ok {
			resultChan <- jsResult{err: fmt.Errorf("postProcessResponse is not a function")}
			return
		}

		result, err := fn(goja.Undefined(), vm.ToValue(jsCtx), vm.ToValue(jsResponse))
		resultChan <- jsResult{result: result, err: err}
	}()

	var result goja.Value
	var err error

	select {
	case res := <-resultChan:
		result, err = res.result, res.err
	// 超时控制
	case <-time.After(jsConfig.ScriptTimeout):
		return statusCode, body, fmt.Errorf("JS postProcess timeout after %v", jsConfig.ScriptTimeout)
	}

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

func (p *JSRuntimePool) hasPostProcessFunction() bool {
	vm := p.Get()
	defer p.Put(vm)
	postProcessFunc := vm.Get("postProcessResponse")
	return postProcessFunc != nil && !goja.IsUndefined(postProcessFunc)
}

func newResponseWriter(w gin.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		statusCode:     200,
		headerMap:      make(http.Header),
		written:        false,
	}
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	return w.body.Write(data)
}

func (w *responseWriter) WriteString(s string) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	return w.body.WriteString(s)
}

func (w *responseWriter) WriteHeader(statusCode int) {
	if w.written {
		return
	}
	w.statusCode = statusCode
	w.written = true

	maps.Copy(w.headerMap, w.ResponseWriter.Header())
}

func (w *responseWriter) Header() http.Header {
	if w.headerMap == nil {
		w.headerMap = make(http.Header)
	}
	return w.headerMap
}

func JSRuntimeMiddleware() gin.HandlerFunc {
	if !jsConfig.Enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	pool := initJSRuntimePool()
	return func(c *gin.Context) {
		start := time.Now()

		// 预处理
		common.SysLog("JS Runtime PreProcessing Request: " + c.Request.Method + " " + c.Request.URL.String())
		if err := pool.PreProcessRequest(c); err != nil {
			common.SysError("JS Runtime PreProcess Error: " + err.Error())
			return
		}
		common.SysLog("JS Runtime PreProcessing Completed")

		// 后处理
		if pool.hasPostProcessFunction() {
			common.SysLog("JS Runtime PostProcessing Response")
			writer := newResponseWriter(c.Writer)
			c.Writer = writer

			c.Next()

			// 后处理响应
			if writer.body.Len() > 0 {
				statusCode, body, err := pool.PostProcessResponse(c, writer.statusCode, writer.body.Bytes())
				if err == nil {
					// 更新响应
					c.Writer = writer.ResponseWriter
					c.Writer.Header().Del("Content-Length")

					// 设置修改后的headers
					for k, v := range writer.headerMap {
						for _, value := range v {
							c.Writer.Header().Add(k, value)
						}
					}

					c.Status(statusCode)
					c.Writer.Write(body)
					common.SysLog(fmt.Sprintf("JS Runtime PostProcessing Completed with status %d", statusCode))
				} else {
					// 出错时返回原始响应
					c.Writer = writer.ResponseWriter
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

func ReloadJSScripts() {
	if jsRuntimePool != nil {
		jsRuntimePool.ReloadScripts()
		common.SysLog("JavaScript scripts reloaded")
	}
}
