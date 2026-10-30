# Maskey's Keychain helper. The Go program runs it as
# `/usr/bin/python3 -I -S -c <this source>`: Apple's interpreter carries the
# com.apple.python3 application identifier, which gives it a data protection
# Keychain access group without a signing identity of Maskey's own, and -I -S
# keep the working directory, PYTHON* variables and site packages from
# replacing the modules imported here.
#
# Protocol: one JSON request on stdin. stdout carries a ready line once the
# host check passes, before any Keychain call, then one JSON result line.
# Nothing else is printed, and no error includes request data or a value.

import base64
import ctypes
import json
import sys

SECURITY = "/System/Library/Frameworks/Security.framework/Security"
CORE_FOUNDATION = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
UTF8 = 0x08000100
APPLE_PYTHON = "com.apple.python3"

ERR_SEC_ITEM_NOT_FOUND = -25300
# Core Foundation counts time from 2001-01-01 UTC, the Unix epoch from 1970.
CF_ABSOLUTE_TIME_EPOCH = 978307200
# What a person sets on an entry besides its value, by request field and
# Keychain attribute. kSecAttrDescription is the item's kind.
DETAILS = (("kind", "kSecAttrDescription"), ("notes", "kSecAttrComment"))


class HostError(Exception):
    pass


class Keychain:
    def __init__(self):
        self.sec = ctypes.CDLL(SECURITY)
        self.cf = ctypes.CDLL(CORE_FOUNDATION)
        self.owned = []
        p = ctypes.c_void_p
        long = ctypes.c_long
        for library, name, result, arguments in [
            (self.cf, "CFStringCreateWithBytes", p, [p, ctypes.c_char_p, long, ctypes.c_uint32, ctypes.c_bool]),
            (self.cf, "CFStringGetLength", long, [p]),
            (self.cf, "CFStringGetMaximumSizeForEncoding", long, [long, ctypes.c_uint32]),
            (self.cf, "CFStringGetCString", ctypes.c_bool, [p, ctypes.c_char_p, long, ctypes.c_uint32]),
            (self.cf, "CFDictionaryCreateMutable", p, [p, long, p, p]),
            (self.cf, "CFDictionarySetValue", None, [p, p, p]),
            (self.cf, "CFDictionaryGetValue", p, [p, p]),
            (self.cf, "CFArrayGetCount", long, [p]),
            (self.cf, "CFArrayGetValueAtIndex", p, [p, long]),
            (self.cf, "CFDataCreate", p, [p, ctypes.c_char_p, long]),
            (self.cf, "CFDataGetLength", long, [p]),
            (self.cf, "CFDataGetBytePtr", p, [p]),
            (self.cf, "CFDateGetAbsoluteTime", ctypes.c_double, [p]),
            (self.cf, "CFRelease", None, [p]),
            (self.sec, "SecTaskCreateFromSelf", p, [p]),
            (self.sec, "SecTaskCopyValueForEntitlement", p, [p, p, p]),
            (self.sec, "SecItemAdd", ctypes.c_int32, [p, p]),
            (self.sec, "SecItemUpdate", ctypes.c_int32, [p, p]),
            (self.sec, "SecItemCopyMatching", ctypes.c_int32, [p, ctypes.POINTER(p)]),
            (self.sec, "SecItemDelete", ctypes.c_int32, [p]),
        ]:
            function = getattr(library, name)
            function.restype = result
            function.argtypes = arguments

    def own(self, pointer):
        if not pointer:
            raise MemoryError("Core Foundation returned no object")
        self.owned.append(pointer)
        return pointer

    def close(self):
        for pointer in reversed(self.owned):
            self.cf.CFRelease(pointer)
        self.owned = []

    def string(self, text):
        data = text.encode("utf-8")
        return self.own(self.cf.CFStringCreateWithBytes(None, data, len(data), UTF8, False))

    def text(self, cf_string):
        size = self.cf.CFStringGetMaximumSizeForEncoding(self.cf.CFStringGetLength(cf_string), UTF8) + 1
        buffer = ctypes.create_string_buffer(size)
        if not self.cf.CFStringGetCString(cf_string, buffer, size, UTF8):
            return None
        return buffer.value.decode("utf-8")

    def data(self, cf_data):
        return ctypes.string_at(self.cf.CFDataGetBytePtr(cf_data), self.cf.CFDataGetLength(cf_data))

    def constant(self, name, library=None):
        return ctypes.c_void_p.in_dll(library or self.sec, name)

    def dictionary(self, pairs):
        dictionary = self.own(self.cf.CFDictionaryCreateMutable(None, 0, None, None))
        for key, value in pairs:
            self.cf.CFDictionarySetValue(dictionary, self.constant(key), value)
        return dictionary

    def check_host(self):
        # The helper only works under Apple's interpreter; this turns the
        # Keychain's later entitlement error into an actionable message.
        task = self.own(self.sec.SecTaskCreateFromSelf(None))
        identifier = self.sec.SecTaskCopyValueForEntitlement(
            task, self.string("com.apple.application-identifier"), None
        )
        if not identifier:
            raise HostError("the interpreter has no application identifier")
        self.own(identifier)
        if self.text(identifier) != APPLE_PYTHON:
            raise HostError("the interpreter is not Apple's com.apple.python3")

    def query(self, service, account=None, extra=()):
        true = self.constant("kCFBooleanTrue", self.cf)
        pairs = [
            ("kSecClass", self.constant("kSecClassGenericPassword")),
            ("kSecAttrService", self.string(service)),
            # Every query selects synchronizable items, which live only in
            # the data protection Keychain that iCloud Keychain syncs.
            ("kSecAttrSynchronizable", true),
        ]
        if account is not None:
            pairs.append(("kSecAttrAccount", self.string(account)))
        return self.dictionary(pairs + list(extra))

    def items(self, service):
        true = self.constant("kCFBooleanTrue", self.cf)
        query = self.query(service, extra=[
            ("kSecReturnAttributes", true),
            ("kSecMatchLimit", self.constant("kSecMatchLimitAll")),
        ])
        result = ctypes.c_void_p()
        status = self.sec.SecItemCopyMatching(query, ctypes.byref(result))
        if status == ERR_SEC_ITEM_NOT_FOUND:
            return 0, []
        if status != 0:
            return status, None
        self.own(result.value)
        get = self.cf.CFDictionaryGetValue
        items = []
        for index in range(self.cf.CFArrayGetCount(result)):
            attributes = self.cf.CFArrayGetValueAtIndex(result, index)
            account = get(attributes, self.constant("kSecAttrAccount"))
            name = self.text(account) if account else None
            if name is None:
                continue
            item = {"account": name}
            modified = get(attributes, self.constant("kSecAttrModificationDate"))
            if modified:
                item["modified"] = self.cf.CFDateGetAbsoluteTime(modified) + CF_ABSOLUTE_TIME_EPOCH
            for field, attribute in DETAILS:
                text = get(attributes, self.constant(attribute))
                if text:
                    item[field] = self.text(text) or ""
            items.append(item)
        return 0, items

    def read(self, service, accounts):
        true = self.constant("kCFBooleanTrue", self.cf)
        values = {}
        for account in accounts:
            query = self.query(service, account, [
                ("kSecReturnData", true),
                ("kSecMatchLimit", self.constant("kSecMatchLimitOne")),
            ])
            result = ctypes.c_void_p()
            status = self.sec.SecItemCopyMatching(query, ctypes.byref(result))
            if status == ERR_SEC_ITEM_NOT_FOUND:
                continue
            if status != 0:
                return status, None
            self.own(result.value)
            values[account] = base64.b64encode(self.data(result)).decode("ascii")
        return 0, values

    def value_data(self, value):
        return self.own(self.cf.CFDataCreate(None, value, len(value)))

    def add(self, service, account, value, details):
        attributes = [
            ("kSecValueData", self.value_data(value)),
            ("kSecAttrLabel", self.string("Maskey: " + account)),
            # Synchronizable items cannot use a ThisDeviceOnly class.
            # AfterFirstUnlock keeps entries usable by unattended jobs while
            # the screen is locked, once the Mac has been unlocked after boot.
            ("kSecAttrAccessible", self.constant("kSecAttrAccessibleAfterFirstUnlock")),
        ]
        for field, attribute in DETAILS:
            if details.get(field):
                attributes.append((attribute, self.string(details[field])))
        return self.sec.SecItemAdd(self.query(service, account, attributes), None)

    def update(self, service, account, value):
        changes = self.dictionary([("kSecValueData", self.value_data(value))])
        return self.sec.SecItemUpdate(self.query(service, account), changes)

    def edit(self, service, account, to, value, details):
        changes = []
        if to != account:
            changes += [
                ("kSecAttrAccount", self.string(to)),
                ("kSecAttrLabel", self.string("Maskey: " + to)),
            ]
        if value is not None:
            changes.append(("kSecValueData", self.value_data(value)))
        if details is not None:
            changes += [(attribute, self.string(details.get(field, ""))) for field, attribute in DETAILS]
        return self.sec.SecItemUpdate(self.query(service, account), self.dictionary(changes))

    def delete(self, service, account):
        return self.sec.SecItemDelete(self.query(service, account))


def handle(keychain, request):
    operation = request["op"]
    service = request["service"]
    if operation == "check":
        return 0, {}
    if operation == "items":
        status, items = keychain.items(service)
        return status, {"items": items}
    if operation == "read":
        status, values = keychain.read(service, request["accounts"])
        return status, {"values": values}
    account = request["account"]
    if operation == "add":
        value = base64.b64decode(request["value"], validate=True)
        return keychain.add(service, account, value, request.get("details", {})), {}
    if operation == "update":
        return keychain.update(service, account, base64.b64decode(request["value"], validate=True)), {}
    if operation == "edit":
        value = request.get("value")
        if value is not None:
            value = base64.b64decode(value, validate=True)
        return keychain.edit(service, account, request["to"], value, request.get("details")), {}
    if operation == "delete":
        return keychain.delete(service, account), {}
    raise ValueError("unknown operation")


def respond(document):
    sys.stdout.write(json.dumps(document) + "\n")
    sys.stdout.flush()


def main():
    try:
        keychain = Keychain()
        keychain.check_host()
    except HostError as error:
        respond({"error": {"stage": "host", "message": str(error)}})
        return 1
    except Exception as error:
        respond({"error": {"stage": "host", "message": type(error).__name__}})
        return 1
    respond({"ready": True})
    try:
        status, result = handle(keychain, json.loads(sys.stdin.buffer.read()))
    except Exception as error:
        # The type name only: messages and tracebacks can quote the request.
        respond({"error": {"stage": "request", "message": type(error).__name__}})
        return 1
    finally:
        keychain.close()
    if status != 0:
        respond({"error": {"stage": "keychain", "os_status": status}})
        return 1
    respond(result)
    return 0


if __name__ == "__main__":
    sys.exit(main())
