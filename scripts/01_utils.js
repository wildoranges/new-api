// Utility functions for JavaScript runtime

function logWithTimestamp(message) {
    const timestamp = new Date().toISOString();
    console.log(`[${timestamp}] ${message}`);
}

function safeJsonParse(str, defaultValue = null) {
    try {
        return JSON.parse(str);
    } catch (e) {
        console.error('JSON parse error:', e.message);
        return defaultValue;
    }
}
