package service

import "errors"

// ErrorCode 用稳定字符串标识不同的非法情形，调用方可据此区分处理。
type ErrorCode string

const (
	ErrCodeNotFound          ErrorCode = "not_found"
	ErrCodeInvalidAmount     ErrorCode = "invalid_amount"
	ErrCodeInsufficientFunds ErrorCode = "insufficient_funds"
	ErrCodeIllegalState      ErrorCode = "illegal_state"
	ErrCodeExceedRemaining   ErrorCode = "exceed_remaining"
	ErrCodeExceedRefundable  ErrorCode = "exceed_refundable"
	ErrCodeAuthExpired       ErrorCode = "auth_expired"
	ErrCodeAdjustBelowUsed   ErrorCode = "adjust_below_used"
	ErrCodeDuplicateMismatch ErrorCode = "duplicate_request_mismatch"
	ErrCodeSettleEmpty       ErrorCode = "settle_empty"
	ErrCodeBatchConfirmed    ErrorCode = "batch_already_confirmed"
	ErrCodeCaptureSettled    ErrorCode = "capture_already_settled"
)

// BizError 携带错误码与可读信息。
type BizError struct {
	Code    ErrorCode
	Message string
}

func (e *BizError) Error() string {
	return string(e.Code) + ": " + e.Message
}

func newBizError(code ErrorCode, msg string) *BizError {
	return &BizError{Code: code, Message: msg}
}

// GetErrorCode 从错误中提取错误码；非业务错误返回空串。
func GetErrorCode(err error) ErrorCode {
	var be *BizError
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}
