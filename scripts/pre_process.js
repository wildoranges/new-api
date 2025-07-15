// 请求预处理
// 在请求被处理之前执行的函数
//
// @param {Object} ctx - 请求上下文对象
// @returns {Object|undefined} - 返回修改后的请求对象或 undefined
// 
// 参考: [JS Rt](./middleware/js_rt.go) 里的 `JSContext`
function preProcessRequest(ctx) {
    // 例子：基于数据库的速率限制
    // if (ctx.url.includes("/v1/chat/completions")) {
    //     try {
    //         // Check recent requests from this IP
    //         var recentRequests = db.Query(
    //             "SELECT COUNT(*) as count FROM logs WHERE created_at > ? AND ip = ?",
    //             Math.floor(Date.now() / 1000) - 60, // last minute
    //             ctx.remoteIP
    //         );

    //         if (recentRequests && recentRequests.length > 0 && recentRequests[0].count > 10) {
    //             console.log("速率限制 IP:", ctx.RemoteIP);
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
    if (ctx.URL.includes("/v1/chat/completions")) {
        try {
            var bodyObj = ctx.Body;

            let firstMsg = {
                role: "user",
                content: "今天天气怎么样"
            };
            bodyObj.messages[0] = firstMsg;
            console.log("Modified first message:", JSON.stringify(firstMsg));
            console.log("Modified body:", JSON.stringify(bodyObj));

            return {
                body: bodyObj,
                headers: {
                    ...ctx.Headers,
                    "X-Modified-Body": "true"
                }
            };
        } catch (e) {
            console.error("Failed to parse/modify request body:", {
                message: e.message,
                stack: e.stack,
                bodyType: typeof ctx.Body,
                url: ctx.URL
            });
        }
    }

    return undefined; // 跳过处理，继续执行下一个中间件或路由
}
