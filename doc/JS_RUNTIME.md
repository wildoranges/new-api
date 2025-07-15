# JavaScript Runtime 中间件

## 配置

将 JavaScript 脚本放置在项目根目录的 `scripts/` 文件夹中：

- `scripts/pre_process.js` - 请求预处理脚本
- `scripts/post_process.js` - 响应后处理脚本

## API 参考

### 预处理函数

```javascript
function preProcessRequest(ctx) {
    // ctx 包含以下属性:
    // - method: 请求方法 (GET, POST, etc.)
    // - url: 请求URL
    // - headers: 请求头 (object)
    // - body: 请求体 (string)
    // - query: 查询参数 (object)
    // - params: 路径参数 (object)
    // - userAgent: User-Agent
    // - remoteIP: 客户端IP
    // - contentType: Content-Type
    // - extra: 额外数据 (object)
    
    // 返回值:
    // - undefined: 继续正常处理
    // - object: 修改请求或阻止请求
    //   - block: true/false - 是否阻止请求
    //   - statusCode: 状态码 (当 block=true 时)
    //   - message: 错误消息 (当 block=true 时)
    //   - headers: 修改的请求头 (object)
    //   - body: 修改的请求体 (string)
}
```

### 后处理函数

```javascript
function postProcessResponse(ctx, response) {
    // ctx: 请求上下文 (同预处理)
    // response 包含以下属性:
    // - statusCode: 响应状态码
    // - headers: 响应头 (object)
    // - body: 响应体 (string)
    
    // 返回值:
    // - undefined: 保持原始响应
    // - object: 修改响应
    //   - statusCode: 新的状态码
    //   - headers: 修改的响应头 (object)
    //   - body: 修改的响应体 (string)
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

### 请求限流

```javascript
function preProcessRequest(ctx) {
    // 基于 IP 的简单限流
    var recentRequests = db.Query(
        "SELECT COUNT(*) as count FROM request_logs WHERE ip = ? AND timestamp > ?",
        ctx.remoteIP, 
        new Date(Date.now() - 60000).toISOString() // 最近1分钟
    );
    
    if (recentRequests[0].count > 100) {
        return {
            block: true,
            statusCode: 429,
            message: "Too many requests"
        };
    }
    
    // 记录请求
    db.Exec(
        "INSERT INTO request_logs (ip, url, timestamp) VALUES (?, ?, ?)",
        ctx.remoteIP, ctx.url, new Date().toISOString()
    );
}
```

### 请求修改

```javascript
function preProcessRequest(ctx) {
    if (ctx.method === "POST" && ctx.body) {
        try {
            var bodyObj = JSON.parse(ctx.body);
            
            // 添加默认参数
            if (!bodyObj.temperature) {
                bodyObj.temperature = 0.7;
            }
            
            // 添加用户标识
            bodyObj._userId = ctx.extra.userId;
            
            return {
                body: JSON.stringify(bodyObj)
            };
        } catch (e) {
            console.error("Failed to parse request body:", e);
        }
    }
}
```

### 响应增强

```javascript
function postProcessResponse(ctx, response) {
    if (response.statusCode === 200 && ctx.url.includes("/v1/chat/completions")) {
        try {
            var bodyObj = JSON.parse(response.body);
            
            // 添加自定义元数据
            bodyObj.metadata = {
                processedAt: new Date().toISOString(),
                version: "1.0.0"
            };
            
            // 记录成功的对话
            db.Exec(
                "INSERT INTO chat_logs (user_ip, model, tokens, timestamp) VALUES (?, ?, ?, ?)",
                ctx.remoteIP, bodyObj.model, bodyObj.usage?.total_tokens || 0, new Date().toISOString()
            );
            
            return {
                statusCode: response.statusCode,
                headers: response.headers,
                body: JSON.stringify(bodyObj)
            };
        } catch (e) {
            console.error("Failed to process chat completion response:", e);
        }
    }
    
    return response;
}
```

## 管理接口

### 重新加载脚本

```bash
curl -X POST http://host:port/api/scripts/reload \
        -H 'Content-Type: application/json' \
        -H 'Authorization Bearer <admin_token>'
```

## 故障排除

- 查看服务日志中的 JavaScript 相关错误信息
- 使用 `console.log()` 调试脚本逻辑
- 确保 JavaScript 语法正确（不支持所有 ES6+ 特性）
