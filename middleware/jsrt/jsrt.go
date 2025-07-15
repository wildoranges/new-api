package jsrt

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"one-api/common"
	"one-api/model"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
)


/// 池化
type JSRuntimePool struct {
	pool       chan *goja.Runtime
	maxSize    int
	createFunc func() *goja.Runtime
	scripts    map[string]string
	mu         sync.RWMutex
	httpClient *http.Client
}

var (
	jsRuntimePool *JSRuntimePool
	jsPoolOnce    sync.Once
)

func NewJSRuntimePool(maxSize int) *JSRuntimePool {
	// 创建HTTP客户端
	httpClient := &http.Client{
		Timeout: jsConfig.FetchTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	pool := &JSRuntimePool{
		pool:       make(chan *goja.Runtime, maxSize),
		maxSize:    maxSize,
		scripts:    make(map[string]string),
		httpClient: httpClient,
	}

	pool.createFunc = func() *goja.Runtime {
		vm := goja.New()
		pool.setupGlobals(vm)
		pool.loadScripts(vm)
		return vm
	}

	// 预创建VM
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
	if vm == nil {
		return
	}
	
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
	console.Set("warn", func(args ...any) {
		var strs []string
		for _, arg := range args {
			strs = append(strs, fmt.Sprintf("%v", arg))
		}
		common.SysError("JS WARN: " + strings.Join(strs, " "))
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

	// fetch 实现
	vm.Set("fetch", func(url string, options ...any) *JSFetchResponse {
		return p.fetch(url, options...)
	})

	// 数据库
	vm.Set("db", &JSDatabase{db: model.DB})

	// 定时器
	vm.Set("setTimeout", func(fn func(), delay int) {
		go func() {
			time.Sleep(time.Duration(delay) * time.Millisecond)
			fn()
		}()
	})
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

func (p *JSRuntimePool) executeWithTimeout(vm *goja.Runtime, fn func() (goja.Value, error)) (goja.Value, error) {
	type result struct {
		value goja.Value
		err   error
	}

	resultChan := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultChan <- result{err: fmt.Errorf("JS panic: %v", r)}
			}
		}()
		
		value, err := fn()
		resultChan <- result{value: value, err: err}
	}()

	select {
	case res := <-resultChan:
		return res.value, res.err
	case <-time.After(jsConfig.ScriptTimeout):
		return nil, fmt.Errorf("script execution timeout after %v", jsConfig.ScriptTimeout)
	}
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

	result, err := p.executeWithTimeout(vm, func() (goja.Value, error) {
		vm.Set("ctx", jsCtx)
		fn, ok := goja.AssertFunction(preProcessFunc)
		if !ok {
			return nil, fmt.Errorf("preProcessRequest is not a function")
		}
		return fn(goja.Undefined(), vm.ToValue(jsCtx))
	})

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

	result, err := p.executeWithTimeout(vm, func() (goja.Value, error) {
		vm.Set("ctx", jsCtx)
		fn, ok := goja.AssertFunction(postProcessFunc)
		if !ok {
			return nil, fmt.Errorf("postProcessResponse is not a function")
		}
		return fn(goja.Undefined(), vm.ToValue(jsCtx), vm.ToValue(jsResponse))
	})

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