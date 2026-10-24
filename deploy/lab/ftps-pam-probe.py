#!/usr/bin/env python3
"""Exercise a PAM service with a supplied test credential, without logging it."""

import ctypes
import ctypes.util
import os
import sys


class PamMessage(ctypes.Structure):
    _fields_ = [("msg_style", ctypes.c_int), ("msg", ctypes.c_char_p)]


class PamResponse(ctypes.Structure):
    _fields_ = [("resp", ctypes.c_void_p), ("resp_retcode", ctypes.c_int)]


class PamConversation(ctypes.Structure):
    pass


ConversationCallback = ctypes.CFUNCTYPE(
    ctypes.c_int,
    ctypes.c_int,
    ctypes.POINTER(ctypes.POINTER(PamMessage)),
    ctypes.POINTER(ctypes.POINTER(PamResponse)),
    ctypes.c_void_p,
)
PamConversation._fields_ = [
    ("conv", ConversationCallback),
    ("appdata_ptr", ctypes.c_void_p),
]


def main() -> int:
    username = os.environ["STEPANEL_PAM_PROBE_USER"].encode()
    password = os.environ["STEPANEL_PAM_PROBE_PASSWORD"].encode()
    service = os.environ["STEPANEL_PAM_PROBE_SERVICE"].encode()

    libpam = ctypes.CDLL(ctypes.util.find_library("pam"))
    libc = ctypes.CDLL(ctypes.util.find_library("c"))
    libc.calloc.argtypes = [ctypes.c_size_t, ctypes.c_size_t]
    libc.calloc.restype = ctypes.c_void_p
    libc.strdup.argtypes = [ctypes.c_char_p]
    libc.strdup.restype = ctypes.c_void_p
    libc.free.argtypes = [ctypes.c_void_p]
    libc.free.restype = None

    def converse(count, messages, response_out, _data):
        raw = libc.calloc(count, ctypes.sizeof(PamResponse))
        if not raw:
            return 5  # PAM_BUF_ERR
        responses = ctypes.cast(raw, ctypes.POINTER(PamResponse))
        for index in range(count):
            style = messages[index].contents.msg_style
            if style == 1:
                responses[index].resp = libc.strdup(password)
            elif style == 2:
                responses[index].resp = libc.strdup(username)
            elif style not in (3, 4):
                libc.free(raw)
                return 19  # PAM_CONV_ERR
        response_out[0] = responses
        return 0

    callback = ConversationCallback(converse)
    conversation = PamConversation(callback, None)
    handle = ctypes.c_void_p()

    libpam.pam_start.argtypes = [
        ctypes.c_char_p,
        ctypes.c_char_p,
        ctypes.POINTER(PamConversation),
        ctypes.POINTER(ctypes.c_void_p),
    ]
    libpam.pam_start.restype = ctypes.c_int
    libpam.pam_authenticate.argtypes = [ctypes.c_void_p, ctypes.c_int]
    libpam.pam_authenticate.restype = ctypes.c_int
    libpam.pam_acct_mgmt.argtypes = [ctypes.c_void_p, ctypes.c_int]
    libpam.pam_acct_mgmt.restype = ctypes.c_int
    libpam.pam_end.argtypes = [ctypes.c_void_p, ctypes.c_int]
    libpam.pam_end.restype = ctypes.c_int

    status = libpam.pam_start(
        service, username, ctypes.byref(conversation), ctypes.byref(handle)
    )
    if status == 0:
        status = libpam.pam_authenticate(handle, 0)
        print("pam_authenticate:", "success" if status == 0 else f"failure ({status})")
        if status == 0:
            status = libpam.pam_acct_mgmt(handle, 0)
            print("pam_acct_mgmt:", "success" if status == 0 else f"failure ({status})")
        libpam.pam_end(handle, status)
    else:
        print("pam_start:", f"failure ({status})")
    return 0 if status == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
