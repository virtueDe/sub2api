package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
)

// ImageRequestMemory follows authentication and precedes middleware that may
// preread the body. It is resource admission, not a business or billing slot.
func ImageRequestMemory() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := strings.TrimSuffix(c.Request.URL.Path, "/async")
		if c.Request.Method != http.MethodPost || (!strings.HasSuffix(path, "/images/generations") && !strings.HasSuffix(path, "/images/edits")) {
			c.Next()
			return
		}
		subject, ok := GetAuthSubjectFromContext(c)
		if !ok {
			c.Next()
			return
		}
		waitCtx, stop := context.WithTimeout(c.Request.Context(), 30*time.Second)
		ctx, release, err := pkghttputil.AdmitImageRequestMemory(waitCtx, subject.UserID, c.Request.ContentLength, strings.TrimSpace(c.GetHeader("Content-Encoding")) != "")
		if err != nil {
			stop()
			if c.Request.Context().Err() != nil {
				RejectRequestBodyReadError(c, "image_memory", err)
				return
			}
			RejectRequestBodyMemoryOverload(c, pkghttputil.ErrRequestBodyMemoryBudget)
			return
		}
		defer release()
		// The admission deadline limits waiting, not the admitted generation.
		ctx = pkghttputil.RequestBodyMemoryContext(c.Request.Context(), ctx)
		stop()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
