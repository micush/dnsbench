//go:build cgo

package main

/*
#cgo LDFLAGS: -lpam
#include <security/pam_appl.h>
#include <stdlib.h>
#include <string.h>

// Conversation callback: answer every "no echo" prompt (the password prompt)
// with the password passed through appdata, and leave anything else blank.
static int dnsbench_conv(int n, const struct pam_message **msg,
                         struct pam_response **resp, void *appdata) {
	const char *pw = (const char *)appdata;
	struct pam_response *r = calloc((size_t)n, sizeof(*r));
	if (r == NULL) return PAM_BUF_ERR;
	for (int i = 0; i < n; i++) {
		if (msg[i]->msg_style == PAM_PROMPT_ECHO_OFF) {
			r[i].resp = strdup(pw);
			if (r[i].resp == NULL) {
				for (int j = 0; j < i; j++) free(r[j].resp);
				free(r);
				return PAM_BUF_ERR;
			}
		}
	}
	*resp = r;
	return PAM_SUCCESS;
}

// Returns PAM_SUCCESS only if the credentials are valid and the account is
// usable (not expired or locked).
static int dnsbench_pam_auth(const char *svc, const char *user, const char *pw) {
	struct pam_conv conv = { dnsbench_conv, (void *)pw };
	pam_handle_t *h = NULL;
	int rc = pam_start(svc, user, &conv, &h);
	if (rc != PAM_SUCCESS) return rc;
	rc = pam_authenticate(h, PAM_DISALLOW_NULL_AUTHTOK);
	if (rc == PAM_SUCCESS) rc = pam_acct_mgmt(h, PAM_DISALLOW_NULL_AUTHTOK);
	pam_end(h, rc);
	return rc;
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

func realPAMAuth(service, user, pass string) bool {
	if user == "" || len(user) > 256 || len(pass) > 1024 ||
		strings.IndexByte(user, 0) >= 0 || strings.IndexByte(pass, 0) >= 0 {
		return false
	}
	cs, cu, cp := C.CString(service), C.CString(user), C.CString(pass)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(cp))
	return C.dnsbench_pam_auth(cs, cu, cp) == C.PAM_SUCCESS
}

const pamAvailable = true
