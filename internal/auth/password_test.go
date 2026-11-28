package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// RFC 6238 附录 B 的 SHA1 测试向量（8 位结果取末 6 位）。
func TestTOTPRFCVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		assert.Equal(t, want, totpCode(key, unix/totpPeriod), unix)
		step, ok := matchTOTP(key, want, time.Unix(unix, 0))
		assert.True(t, ok, unix)
		assert.Equal(t, unix/totpPeriod, step)
	}
	_, ok := matchTOTP(key, "287082", time.Unix(59+3*totpPeriod, 0))
	assert.False(t, ok, "超出前后一个时间步的容忍窗口")
}

func TestPasswordHash(t *testing.T) {
	h, err := hashPassword("correct horse battery")
	assert.NoError(t, err)
	assert.True(t, verifyPassword(h, "correct horse battery"))
	assert.False(t, verifyPassword(h, "correct horse batterY"))
	assert.False(t, verifyPassword("$argon2id$garbage", "correct horse battery"))
	h2, err := hashPassword("correct horse battery")
	assert.NoError(t, err)
	assert.NotEqual(t, h, h2, "每次加盐")
}
