package handlers

import "github.com/gin-gonic/gin"

// SetSSEHeaders prepares an unbuffered SSE response without writing or flushing it.
// Callers can still return an HTTP error until the first payload or heartbeat is sent.
func SetSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
}
