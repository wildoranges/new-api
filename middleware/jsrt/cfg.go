package jsrt

import (
	"os"
	"strconv"
	"time"
)

// / Runtime 配置
type JSRuntimeConfig struct {
	Enabled        bool          `json:"enabled"`
	MaxVMCount     int           `json:"max_vm_count"`
	ScriptTimeout  time.Duration `json:"script_timeout"`
	PreScriptPath  string        `json:"pre_script_path"`
	PostScriptPath string        `json:"post_script_path"`
	FetchTimeout   time.Duration `json:"fetch_timeout"`
}

var (
	jsConfig = JSRuntimeConfig{}
)

const (
	defaultPreScriptPath  = "scripts/pre_process.js"
	defaultPostScriptPath = "scripts/post_process.js"
	defaultScriptTimeout  = 5 * time.Second
	defaultFetchTimeout   = 10 * time.Second
	defaultMaxVMCount     = 8
)

func loadCfg() {
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

	if fetchTimeout := os.Getenv("JS_FETCH_TIMEOUT"); fetchTimeout != "" {
		if t, err := time.ParseDuration(fetchTimeout + "s"); err == nil && t > 0 {
			jsConfig.FetchTimeout = t
		}
	} else {
		jsConfig.FetchTimeout = defaultFetchTimeout
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
