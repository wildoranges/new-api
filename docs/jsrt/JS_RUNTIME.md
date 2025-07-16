# JavaScript Runtime 中间件

## 配置

将 JavaScript 脚本放置在项目根目录的 `scripts/` 文件夹中：

- `scripts/pre_process.js` - 请求预处理脚本
- `scripts/post_process.js` - 响应后处理脚本

## API 参考

### 预处理函数

```javascript
function preProcessRequest(req) {
    // req 包含以下属性:
    // - method: 请求方法 (GET, POST, etc.)
    // - url: 请求URL
    // - headers: 请求头 (object)
    // - body: 请求体 (object/string/ArrayBuffer)
    // - remoteIP: 客户端IP
    // - extra: 额外数据 (object)
    
    // 返回值:
    // - undefined: 继续正常处理
    // - object: 修改请求或阻止请求
    //   - block: true/false - 是否阻止请求
    //   - statusCode: 状态码
    //   - message: 错误消息
    //   - headers: 修改的请求头 (object)
    //   - body: 修改的请求体
}
```

### 后处理函数

```javascript
function postProcessResponse(req, response) {
    // ctx: 请求上下文 (同预处理)
    // response 包含以下属性:
    // - statusCode: 响应状态码
    // - headers: 响应头 (object)
    // - body: 响应体
    
    // 返回值:
    // - undefined: 保持原始响应
    // - object: 修改响应
    //   - statusCode: 新的状态码
    //   - headers: 修改的响应头
    //   - body: 修改的响应体
}
```

### 数据库对象

```javascript
// 查询数据库
var results = db.Query("SELECT * FROM users WHERE id = ?", 123);

// 执行 SQL
var result = db.Exec("UPDATE users SET last_login = NOW() WHERE id = ?", 123);
// result 包含: { rowsAffected: number, error: any }
```

### 全局对象

- `console.log()` - 输出日志
- `console.error()` - 输出错误日志
- `JSON.parse()` - 解析 JSON
- `JSON.stringify()` - 序列化为 JSON

## 使用示例

```js
// 例子：基于数据库的速率限制
if (req.url.includes("/v1/chat/completions")) {
    try {
        // Check recent requests from this IP
        var recentRequests = db.query(
            "SELECT COUNT(*) as count FROM logs WHERE created_at > ? AND ip = ?",
            Math.floor(Date.now() / 1000) - 60, // last minute
            req.remoteIP
        );
        if (recentRequests && recentRequests.length > 0 && recentRequests[0].count > 10) {
            console.log("速率限制 IP:", req.remoteIP);
            return {
                block: true,
                statusCode: 429,
                message: "超过速率限制"
            };
        }
    } catch (e) {
        console.error("Ratelimit 数据库错误:", e);
    }
}


// 例子：修改请求
if (req.url.includes("/chat/completions")) {
    try {
        var bodyObj = req.body;
        let firstMsg = { // 需要新建一个对象，不能修改原有对象
            role: "user",
            content: "喵呜🐱～嘻嘻"
        };
        bodyObj.messages[0] = firstMsg;
        console.log("Modified first message:", JSON.stringify(firstMsg));
        console.log("Modified body:", JSON.stringify(bodyObj));
        return {
            body: bodyObj,
            headers: {
                ...req.headers,
                "X-Modified-Body": "true"
            }
        };
    } catch (e) {
        console.error("Failed to modify request body:", {
            message: e.message,
            stack: e.stack,
            bodyType: typeof req.body,
            url: req.url
        });
    }
}


// 例子：读取最近一条日志，新增 jsrt 日志，并输出日志总数
try {
    // 1. 读取最近一条日志
    var recentLogs = logdb.query(
        "SELECT id, user_id, username, content, created_at FROM logs ORDER BY id DESC LIMIT 1"
    );
    var recentLog = null;
    if (recentLogs && recentLogs.length > 0) {
        recentLog = recentLogs[0];
        console.log("最近一条日志:", JSON.stringify(recentLog));
    }
    // 2. 新增一条 jsrt 日志
    var currentTimestamp = Math.floor(Date.now() / 1000);
    var jsrtLogContent = "JSRT 预处理中间件执行 - " + req.URL + " - " + new Date().toISOString();
    var insertResult = logdb.exec(
        "INSERT INTO logs (user_id, username, created_at, type, content) VALUES (?, ?, ?, ?, ?)",
        req.UserID || 0,
        req.Username || "jsrt-system",
        currentTimestamp,
        4, // LogTypeSystem
        jsrtLogContent
    );
    if (insertResult.error) {
        console.error("插入 JSRT 日志失败:", insertResult.error);
    } else {
        console.log("成功插入 JSRT 日志，影响行数:", insertResult.rowsAffected);
    }
    // 3. 输出日志总数
    var totalLogsResult = logdb.query("SELECT COUNT(*) as total FROM logs");
    var totalLogs = 0;
    if (totalLogsResult && totalLogsResult.length > 0) {
        totalLogs = totalLogsResult[0].total;
    }
    console.log("当前日志总数:", totalLogs);
    console.log("JSRT 日志管理示例执行完成");
} catch (e) {
    console.error("JSRT 日志管理示例执行失败:", {
        message: e.message,
        stack: e.stack,
        url: req.URL
    });
}


// 例子：使用 fetch 调用外部 API
if (req.url.includes("/api/uptime/status")) {
    try {
        // 使用 httpbin.org/ip 测试 fetch 功能
        var response = fetch("https://httpbin.org/ip", {
            method: "GET",
            timeout: 5, // 5秒超时
            headers: {
                "User-Agent": "OneAPI-JSRT/1.0"
            }
        });
        if (response.Error.length === 0) {
            // 解析响应体
            var ipData = JSON.parse(response.Body);
            // 可以根据获取到的 IP 信息进行后续处理
            if (ipData.origin) {
                console.log("外部 IP 地址:", ipData.origin);
                // 示例：记录 IP 信息到数据库
                var currentTimestamp = Math.floor(Date.now() / 1000);
                var logContent = "Fetch 示例 - 外部 IP: " + ipData.origin + " - " + new Date().toISOString();
                var insertResult = logdb.exec(
                    "INSERT INTO logs (user_id, username, created_at, type, content) VALUES (?, ?, ?, ?, ?)",
                    0,
                    "jsrt-fetch",
                    currentTimestamp,
                    4, // LogTypeSystem
                    logContent
                );
                if (insertResult.error) {
                    console.error("记录 IP 信息失败:", insertResult.error);
                } else {
                    console.log("成功记录 IP 信息到数据库");
                }
            }
        } else {
            console.error("Fetch 失败 ", response.Status, " ", response.Error);
        }
    } catch (e) {
        console.error("Fetch 失败:", {
            message: e.message,
            stack: e.stack,
            url: req.url
        });
    }
}
```

## 管理接口

### 重新加载脚本

```bash
curl -X POST http://host:port/api/jsrt/reload \
        -H 'Content-Type: application/json' \
        -H 'Authorization Bearer <admin_token>'
```

## 故障排除

- 查看服务日志中的 JavaScript 相关错误信息
- 使用 `console.log()` 调试脚本逻辑
- 确保 JavaScript 语法正确（不支持所有 ES6+ 特性）
