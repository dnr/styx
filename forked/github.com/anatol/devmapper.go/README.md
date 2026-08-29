# Pure Go library for device mapper targets management

`devmapper.go` is a pure-Go library that helps to deal with device mapper targets.

Here is an example that demonstrates the API usage:
```go
func main() {
    name := "crypttarget"
    uuid := "2f144136-b0de-4b51-b2eb-bd869cc39a6e"
    key := make([]byte, 32)
    c := devmapper.CryptTable{
        Length:        60000 * 512, // size of the device in bytes
        Encryption:    "aes-xts-plain64",
        Key:           key,
        BackendDevice: "/dev/loop0",
        Flags:         []string{devmapper.CryptFlagAllowDiscards},
    }
    if err := devmapper.CreateAndLoad(name, uuid, c); err != nil {
        // handle error
    }
    defer devmapper.Remove(name)

    // at this point a devmapper target named 'crypttarget' should exist
    // you can check it with 'dmsetup info crypttarget'
    // and udev will create /dev/mapper/crypttarget device file
}
```

Or the same crypttarget initialization using Linux keychain

```go
func main() {
    // load key into keyring
    keyname := fmt.Sprintf("cryptsetup:%s-d%d", uuid, luksDigestId) // an example of keyname used by LUKS framework
    kid, err := unix.AddKey("logon", keyname, key, unix.KEY_SPEC_THREAD_KEYRING)
    if err != nil {
        return err
    }
    defer unlinkKey(kid)
    keyid := fmt.Sprintf(":%v:logon:%v", len(key), keyname)

    name := "crypttarget"
    uuid := "2f144136-b0de-4b51-b2eb-bd869cc39a6e"
    c := devmapper.CryptTable{
        Length:        60000 * 512, // size of the device in bytes
        Encryption:    "aes-xts-plain64",
        KeyID:         keyid,
        BackendDevice: "/dev/loop0",
        Flags:         []string{devmapper.CryptFlagAllowDiscards},
    }
    if err := devmapper.CreateAndLoad(name, uuid, c); err != nil {
        // handle error
    }
    defer devmapper.Remove(name)
}

func unlinkKey(kid int) {
	if _, err := unix.KeyctlInt(unix.KEYCTL_REVOKE, kid, 0, 0, 0); err != nil {
		fmt.Printf("key revoke: %v\n", err)
	}

	if _, err := unix.KeyctlInt(unix.KEYCTL_UNLINK, kid, unix.KEY_SPEC_THREAD_KEYRING, 0, 0); err != nil {
		fmt.Printf("key unlink, thread: %v\n", err)
	}

	// We added key to thread keyring only. But let's try to unlink the key from other keyrings as well just to be safe
	_, _ = unix.KeyctlInt(unix.KEYCTL_UNLINK, kid, unix.KEY_SPEC_PROCESS_KEYRING, 0, 0)
	_, _ = unix.KeyctlInt(unix.KEYCTL_UNLINK, kid, unix.KEY_SPEC_USER_KEYRING, 0, 0)
}
```

## License

See [LICENSE](LICENSE).
