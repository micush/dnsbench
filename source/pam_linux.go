//go:build linux && cgo

package main

/*
#cgo LDFLAGS: -lpam
#include <security/pam_appl.h>
#include <stdlib.h>
#include <string.h>

struct ddgw_cred { const char *pass; };

// Answers every password/echo prompt with the supplied password; text and
// error messages are ignored.  No callbacks into Go.
static int ddgw_conv(int n, const struct pam_message **msg,
                     struct pam_response **resp, void *data) {
	struct ddgw_cred *c = (struct ddgw_cred *)data;
	struct pam_response *r = calloc((size_t)n, sizeof(*r));
	if (!r) return PAM_BUF_ERR;
	for (int i = 0; i < n; i++) {
		switch (msg[i]->msg_style) {
		case PAM_PROMPT_ECHO_OFF:
		case PAM_PROMPT_ECHO_ON:
			r[i].resp = strdup(c->pass);
			if (!r[i].resp) goto fail;
			break;
		case PAM_ERROR_MSG:
		case PAM_TEXT_INFO:
			break;
		default:
			goto fail;
		}
	}
	*resp = r;
	return PAM_SUCCESS;
fail:
	for (int i = 0; i < n; i++) {
		if (r[i].resp) {
			memset(r[i].resp, 0, strlen(r[i].resp));
			free(r[i].resp);
		}
	}
	free(r);
	return PAM_CONV_ERR;
}

// Returns a PAM status code; PAM_SUCCESS only if the user authenticated and
// the account is valid (not expired/locked).
static int ddgw_pam_auth(const char *service, const char *user, const char *pass) {
	struct ddgw_cred c = { pass };
	struct pam_conv conv = { ddgw_conv, &c };
	pam_handle_t *h = NULL;
	int rc = pam_start(service, user, &conv, &h);
	if (rc != PAM_SUCCESS) return rc;
	rc = pam_authenticate(h, PAM_DISALLOW_NULL_AUTHTOK);
	if (rc == PAM_SUCCESS) rc = pam_acct_mgmt(h, PAM_DISALLOW_NULL_AUTHTOK);
	pam_end(h, rc);
	return rc;
}
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

const pamAvailable = true

// pamSem bounds concurrent PAM conversations (each may fork a helper).
var pamSem = make(chan struct{}, 4)

// pamAuthenticate checks user/password against the PAM service.  Any failure
// (bad password, locked/expired account, missing service) is an error.
func pamAuthenticate(service, user, password string) error {
	if user == "" || password == "" || strings.ContainsRune(user, 0) || strings.ContainsRune(password, 0) {
		return errors.New("empty credentials")
	}
	pamSem <- struct{}{}
	defer func() { <-pamSem }()

	cs, cu, cp := C.CString(service), C.CString(user), C.CString(password)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(cu))
	defer func() {
		C.memset(unsafe.Pointer(cp), 0, C.size_t(len(password)))
		C.free(unsafe.Pointer(cp))
	}()
	if rc := C.ddgw_pam_auth(cs, cu, cp); rc != C.PAM_SUCCESS {
		return errors.New("pam: " + C.GoString(C.pam_strerror(nil, rc)))
	}
	return nil
}
