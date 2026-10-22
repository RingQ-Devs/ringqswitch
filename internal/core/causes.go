package core

import (
	"strconv"
	"strings"
)

// Cause is a hangup cause. Values below 128 are ITU-T Q.850 cause codes;
// larger values are switch specific.
type Cause int

const (
	CauseNone                    Cause = 0
	CauseUnallocatedNumber       Cause = 1
	CauseNoRouteDestination      Cause = 3
	CauseNormalClearing          Cause = 16
	CauseUserBusy                Cause = 17
	CauseNoUserResponse          Cause = 18
	CauseNoAnswer                Cause = 19
	CauseSubscriberAbsent        Cause = 20
	CauseCallRejected            Cause = 21
	CauseNumberChanged           Cause = 22
	CauseRedirectionToNewDest    Cause = 23
	CauseExchangeRoutingError    Cause = 25
	CauseDestinationOutOfOrder   Cause = 27
	CauseInvalidNumberFormat     Cause = 28
	CauseFacilityRejected        Cause = 29
	CauseNormalUnspecified       Cause = 31
	CauseNormalCircuitCongestion Cause = 34
	CauseNetworkOutOfOrder       Cause = 38
	CauseNormalTemporaryFailure  Cause = 41
	CauseSwitchCongestion        Cause = 42
	CauseServiceUnavailable      Cause = 63
	CauseServiceNotImplemented   Cause = 79
	CauseIncompatibleDestination Cause = 88
	CauseRecoveryOnTimerExpire   Cause = 102
	CauseInterworking            Cause = 127
	CauseOriginatorCancel        Cause = 487
	CauseLoseRace                Cause = 502
	CauseManagerRequest          Cause = 503
	CauseAllottedTimeout         Cause = 602
	CauseMediaTimeout            Cause = 604
	CauseUserNotRegistered       Cause = 606
	CauseInvalidGateway          Cause = 608
	CauseGatewayDown             Cause = 609
	CauseInvalidURL              Cause = 610
	CauseSystemShutdown          Cause = 701
	CauseBlindTransfer           Cause = 800
)

var causeNames = map[Cause]string{
	CauseNone:                    "NONE",
	CauseUnallocatedNumber:       "UNALLOCATED_NUMBER",
	CauseNoRouteDestination:      "NO_ROUTE_DESTINATION",
	CauseNormalClearing:          "NORMAL_CLEARING",
	CauseUserBusy:                "USER_BUSY",
	CauseNoUserResponse:          "NO_USER_RESPONSE",
	CauseNoAnswer:                "NO_ANSWER",
	CauseSubscriberAbsent:        "SUBSCRIBER_ABSENT",
	CauseCallRejected:            "CALL_REJECTED",
	CauseNumberChanged:           "NUMBER_CHANGED",
	CauseRedirectionToNewDest:    "REDIRECTION_TO_NEW_DESTINATION",
	CauseExchangeRoutingError:    "EXCHANGE_ROUTING_ERROR",
	CauseDestinationOutOfOrder:   "DESTINATION_OUT_OF_ORDER",
	CauseInvalidNumberFormat:     "INVALID_NUMBER_FORMAT",
	CauseFacilityRejected:        "FACILITY_REJECTED",
	CauseNormalUnspecified:       "NORMAL_UNSPECIFIED",
	CauseNormalCircuitCongestion: "NORMAL_CIRCUIT_CONGESTION",
	CauseNetworkOutOfOrder:       "NETWORK_OUT_OF_ORDER",
	CauseNormalTemporaryFailure:  "NORMAL_TEMPORARY_FAILURE",
	CauseSwitchCongestion:        "SWITCH_CONGESTION",
	CauseServiceUnavailable:      "SERVICE_UNAVAILABLE",
	CauseServiceNotImplemented:   "SERVICE_NOT_IMPLEMENTED",
	CauseIncompatibleDestination: "INCOMPATIBLE_DESTINATION",
	CauseRecoveryOnTimerExpire:   "RECOVERY_ON_TIMER_EXPIRE",
	CauseInterworking:            "INTERWORKING",
	CauseOriginatorCancel:        "ORIGINATOR_CANCEL",
	CauseLoseRace:                "LOSE_RACE",
	CauseManagerRequest:          "MANAGER_REQUEST",
	CauseAllottedTimeout:         "ALLOTTED_TIMEOUT",
	CauseMediaTimeout:            "MEDIA_TIMEOUT",
	CauseUserNotRegistered:       "USER_NOT_REGISTERED",
	CauseInvalidGateway:          "INVALID_GATEWAY",
	CauseGatewayDown:             "GATEWAY_DOWN",
	CauseInvalidURL:              "INVALID_URL",
	CauseSystemShutdown:          "SYSTEM_SHUTDOWN",
	CauseBlindTransfer:           "BLIND_TRANSFER",
}

var causeByName = func() map[string]Cause {
	m := map[string]Cause{}
	for c, n := range causeNames {
		m[n] = c
	}
	return m
}()

func (c Cause) String() string {
	if n, ok := causeNames[c]; ok {
		return n
	}
	return "CAUSE_" + strconv.Itoa(int(c))
}

// ParseCause accepts a cause name or number; unknown input yields
// NORMAL_CLEARING.
func ParseCause(s string) Cause {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return CauseNormalClearing
	}
	if c, ok := causeByName[s]; ok {
		return c
	}
	if n, err := strconv.Atoi(s); err == nil {
		return Cause(n)
	}
	return CauseNormalClearing
}

// Q850 returns the value for a Reason: Q.850 header.
func (c Cause) Q850() int {
	switch {
	case c > 0 && c < 128:
		return int(c)
	case c == CauseUserNotRegistered:
		return int(CauseSubscriberAbsent)
	case c == CauseMediaTimeout:
		return int(CauseRecoveryOnTimerExpire)
	case c == CauseGatewayDown, c == CauseInvalidGateway:
		return int(CauseNetworkOutOfOrder)
	}
	return int(CauseNormalClearing)
}

// SIPCode maps a cause to the response sent on an unanswered inbound call.
func (c Cause) SIPCode() int {
	switch c {
	case CauseUnallocatedNumber, CauseNoRouteDestination:
		return 404
	case CauseUserBusy:
		return 486
	case CauseNoUserResponse, CauseRecoveryOnTimerExpire:
		return 408
	case CauseNoAnswer, CauseSubscriberAbsent, CauseUserNotRegistered, CauseNormalClearing:
		return 480
	case CauseCallRejected:
		return 603
	case CauseNumberChanged:
		return 410
	case CauseExchangeRoutingError:
		return 483
	case CauseDestinationOutOfOrder, CauseNetworkOutOfOrder, CauseGatewayDown:
		return 502
	case CauseInvalidNumberFormat, CauseInvalidURL:
		return 484
	case CauseFacilityRejected, CauseServiceNotImplemented:
		return 501
	case CauseNormalCircuitCongestion, CauseSwitchCongestion, CauseNormalTemporaryFailure,
		CauseServiceUnavailable, CauseSystemShutdown:
		return 503
	case CauseIncompatibleDestination:
		return 488
	case CauseOriginatorCancel:
		return 487
	}
	return 500
}

// CauseFromSIP maps a failure response code received on an outbound leg.
func CauseFromSIP(code int) Cause {
	switch code {
	case 200:
		return CauseNormalClearing
	case 401, 402, 403, 407, 603, 608:
		return CauseCallRejected
	case 404:
		return CauseUnallocatedNumber
	case 485, 604:
		return CauseNoRouteDestination
	case 408, 504:
		return CauseRecoveryOnTimerExpire
	case 410:
		return CauseNumberChanged
	case 413, 414, 416, 420, 421, 423, 505, 513:
		return CauseInterworking
	case 480:
		return CauseNoUserResponse
	case 400, 481, 500, 503:
		return CauseNormalTemporaryFailure
	case 486, 600:
		return CauseUserBusy
	case 484:
		return CauseInvalidNumberFormat
	case 488, 606:
		return CauseIncompatibleDestination
	case 502:
		return CauseNetworkOutOfOrder
	case 405:
		return CauseServiceUnavailable
	case 406, 415, 501:
		return CauseServiceNotImplemented
	case 482, 483:
		return CauseExchangeRoutingError
	case 487:
		return CauseOriginatorCancel
	}
	return CauseNormalUnspecified
}
