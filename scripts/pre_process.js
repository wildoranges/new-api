// 请求预处理
// 在请求被处理之前执行的函数
//
// @param {Object} req - 请求对象
// @returns {Object|undefined} - 返回修改后的请求对象或 undefined
// 
// 参考: [JS Rt](./middleware/jsrt/req.go) 里的 `JSReq`
function preProcessRequest(req) {
    // 例子：基于数据库的速率限制
    // if (req.url.includes("/v1/chat/completions")) {
    //     try {
    //         // Check recent requests from this IP
    //         var recentRequests = db.query(
    //             "SELECT COUNT(*) as count FROM logs WHERE created_at > ? AND ip = ?",
    //             Math.floor(Date.now() / 1000) - 60, // last minute
    //             req.remoteIP
    //         );

    //         if (recentRequests && recentRequests.length > 0 && recentRequests[0].count > 10) {
    //             console.log("速率限制 IP:", req.remoteIP);
    //             return {
    //                 block: true,
    //                 statusCode: 429,
    //                 message: "超过速率限制"
    //             };
    //         }
    //     } catch (e) {
    //         console.error("Ratelimit 数据库错误:", e);
    //     }
    // }

    // 例子：修改请求
    // if (req.url.includes("/chat/completions")) {
    //     try {
    //         var bodyObj = req.body;

    //         let firstMsg = { // 需要新建一个对象，不能修改原有对象
    //             role: "user",
    //             content: "喵呜🐱～嘻嘻"
    //         };
    //         bodyObj.messages[0] = firstMsg;
    //         console.log("Modified first message:", JSON.stringify(firstMsg));
    //         console.log("Modified body:", JSON.stringify(bodyObj));

    //         return {
    //             body: bodyObj,
    //             headers: {
    //                 ...req.headers,
    //                 "X-Modified-Body": "true"
    //             }
    //         };
    //     } catch (e) {
    //         console.error("Failed to modify request body:", {
    //             message: e.message,
    //             stack: e.stack,
    //             bodyType: typeof req.body,
    //             url: req.url
    //         });
    //     }
    // }

    // 例子：读取最近一条日志，新增 jsrt 日志，并输出日志总数
    // try {
    //     // 1. 读取最近一条日志
    //     var recentLogs = logdb.query(
    //         "SELECT id, user_id, username, content, created_at FROM logs ORDER BY id DESC LIMIT 1"
    //     );

    //     var recentLog = null;
    //     if (recentLogs && recentLogs.length > 0) {
    //         recentLog = recentLogs[0];
    //         console.log("最近一条日志:", JSON.stringify(recentLog));
    //     }

    //     // 2. 新增一条 jsrt 日志
    //     var currentTimestamp = Math.floor(Date.now() / 1000);
    //     var jsrtLogContent = "JSRT 预处理中间件执行 - " + req.URL + " - " + new Date().toISOString();

    //     var insertResult = logdb.exec(
    //         "INSERT INTO logs (user_id, username, created_at, type, content) VALUES (?, ?, ?, ?, ?)",
    //         req.UserID || 0,
    //         req.Username || "jsrt-system",
    //         currentTimestamp,
    //         4, // LogTypeSystem
    //         jsrtLogContent
    //     );

    //     if (insertResult.error) {
    //         console.error("插入 JSRT 日志失败:", insertResult.error);
    //     } else {
    //         console.log("成功插入 JSRT 日志，影响行数:", insertResult.rowsAffected);
    //     }

    //     // 3. 输出日志总数
    //     var totalLogsResult = logdb.query("SELECT COUNT(*) as total FROM logs");
    //     var totalLogs = 0;
    //     if (totalLogsResult && totalLogsResult.length > 0) {
    //         totalLogs = totalLogsResult[0].total;
    //     }

    //     console.log("当前日志总数:", totalLogs);
    //     console.log("JSRT 日志管理示例执行完成");

    // } catch (e) {
    //     console.error("JSRT 日志管理示例执行失败:", {
    //         message: e.message,
    //         stack: e.stack,
    //         url: req.URL
    //     });
    // }

    // 例子：使用 fetch 调用外部 API
    // if (req.url.includes("/api/uptime/status")) {
    //     try {
    //         // 使用 httpbin.org/ip 测试 fetch 功能
    //         var response = fetch("https://httpbin.org/ip", {
    //             method: "GET",
    //             timeout: 5, // 5秒超时
    //             headers: {
    //                 "User-Agent": "OneAPI-JSRT/1.0"
    //             }
    //         });

    //         if (response.Error.length === 0) {
    //             // 解析响应体
    //             var ipData = JSON.parse(response.Body);

    //             // 可以根据获取到的 IP 信息进行后续处理
    //             if (ipData.origin) {
    //                 console.log("外部 IP 地址:", ipData.origin);

    //                 // 示例：记录 IP 信息到数据库
    //                 var currentTimestamp = Math.floor(Date.now() / 1000);
    //                 var logContent = "Fetch 示例 - 外部 IP: " + ipData.origin + " - " + new Date().toISOString();

    //                 var insertResult = logdb.exec(
    //                     "INSERT INTO logs (user_id, username, created_at, type, content) VALUES (?, ?, ?, ?, ?)",
    //                     0,
    //                     "jsrt-fetch",
    //                     currentTimestamp,
    //                     4, // LogTypeSystem
    //                     logContent
    //                 );

    //                 if (insertResult.error) {
    //                     console.error("记录 IP 信息失败:", insertResult.error);
    //                 } else {
    //                     console.log("成功记录 IP 信息到数据库");
    //                 }
    //             }
    //         } else {
    //             console.error("Fetch 失败 ", response.Status, " ", response.Error);
    //         }
    //     } catch (e) {
    //         console.error("Fetch 失败:", {
    //             message: e.message,
    //             stack: e.stack,
    //             url: req.url
    //         });
    //     }
    // }

    return undefined; // 跳过处理，继续执行下一个中间件或路由
}
