package domain

import "errors"

var (
	ErrAccountNotFound       = errors.New("account not found")
	ErrPreAuthNotFound       = errors.New("preauth not found")
	ErrInsufficientFunds     = errors.New("insufficient available balance")
	ErrAmountExceedsHeld     = errors.New("amount exceeds held amount")
	ErrAmountExceedsCaptured = errors.New("amount exceeds captured amount")
	ErrInvalidState          = errors.New("illegal state transition")
	ErrPreAuthExpired        = errors.New("preauth expired")
	ErrDuplicateRequest      = errors.New("duplicate request")
	ErrAlreadySettled        = errors.New("capture already settled")
	ErrSettlementInProgress  = errors.New("settlement batch in progress")
	ErrInvalidAmount         = errors.New("invalid amount")
	ErrNotFound              = errors.New("not found")
)
