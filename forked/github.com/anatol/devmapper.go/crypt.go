package devmapper

import (
	"crypto/aes"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/xts"
)

const (
	// flags for crypt target

	// CryptFlagAllowDiscards is an equivalent of 'allow_discards' crypt option
	CryptFlagAllowDiscards = "allow_discards"
	// CryptFlagSameCPUCrypt is an equivalent of 'same_cpu_crypt' crypt option
	CryptFlagSameCPUCrypt = "same_cpu_crypt"
	// CryptFlagSubmitFromCryptCPUs is an equivalent of 'submit_from_crypt_cpus' crypt option
	CryptFlagSubmitFromCryptCPUs = "submit_from_crypt_cpus"
	// CryptFlagNoReadWorkqueue is an equivalent of 'no_read_workqueue' crypt option
	CryptFlagNoReadWorkqueue = "no_read_workqueue"
	// CryptFlagNoWriteWorkqueue is an equivalent of 'no_write_workqueue' crypt option
	CryptFlagNoWriteWorkqueue = "no_write_workqueue"
)

// CryptTable represents information needed for 'crypt' target creation
type CryptTable struct {
	Start         uint64
	Length        uint64
	BackendDevice string // device that stores the encrypted data
	BackendOffset uint64
	Encryption    string
	Key           []byte
	KeyID         string // key id in the keystore e.g. ":32:logon:foobarkey"
	IVTweak       uint64
	Flags         []string // TODO: maybe convert it to bitflag instead?
	SectorSize    uint64   // size of the sector the crypto device operates with
}

func (c CryptTable) start() uint64 {
	return c.Start
}

func (c CryptTable) length() uint64 {
	return c.Length
}

func (c CryptTable) targetType() string {
	return "crypt"
}

func (c CryptTable) buildSpec() string {
	key := c.KeyID
	if key == "" {
		// dm-crypt requires hex-encoded password
		key = hex.EncodeToString(c.Key)
	}

	flags := c.Flags
	if c.SectorSize != 0 && c.SectorSize != SectorSize {
		flags = append(flags, "sector_size:"+strconv.Itoa(int(c.SectorSize)))
	}
	args := []string{c.Encryption, key, strconv.FormatUint(c.IVTweak, 10), c.BackendDevice, strconv.FormatUint(c.BackendOffset/SectorSize, 10)}
	args = append(args, strconv.Itoa(len(flags)))
	args = append(args, flags...)

	return strings.Join(args, " ")
}

type cryptVolume struct {
	f          *os.File
	offset     uint64
	sectorSize uint64
	cipher     *xts.Cipher
}

func (c CryptTable) makeCipher() (*xts.Cipher, error) {
	if c.Encryption != "aes-xts-plain64" {
		return nil, fmt.Errorf("unsupported cipher suite '%s'", c.Encryption)
	}
	return xts.NewCipher(aes.NewCipher, c.Key)
}

func (c CryptTable) openVolume(flag int, perm fs.FileMode) (Volume, error) {
	sectorSize := c.SectorSize
	if sectorSize == 0 {
		sectorSize = SectorSize
	} else if sectorSize%SectorSize != 0 {
		return nil, fmt.Errorf("crypto sector size must be multiple of devmapper.SectorSize")
	}

	if c.KeyID != "" {
		return nil, fmt.Errorf("crypto userspace volume does not work with kernel keychain login")
	}

	cipher, err := c.makeCipher()
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(c.BackendDevice, flag, perm)
	if err != nil {
		return nil, err
	}
	return &cryptVolume{f: file, offset: c.BackendOffset, sectorSize: sectorSize, cipher: cipher}, nil
}

func (c cryptVolume) ReadAt(buf []byte, off int64) (int, error) {
	length := uint64(len(buf))
	offset := uint64(off)

	if length%c.sectorSize != 0 {
		return 0, fmt.Errorf("size of the buffer must be multiple of CryptTable.SectorSize")
	}
	if offset%c.sectorSize != 0 {
		return 0, fmt.Errorf("offset must be multiple of CryptTable.SectorSize")
	}

	cryptBuf := make([]byte, length)
	if _, err := c.f.ReadAt(cryptBuf, off+int64(c.offset)); err != nil {
		return 0, err
	}

	sectorSize := int(c.sectorSize)
	counter := uint64(off) / uint64(SectorSize)
	for i := 0; i < int(length); i += sectorSize {
		ciphertext := cryptBuf[i : i+sectorSize]
		plaintext := buf[i : i+sectorSize]
		c.cipher.Decrypt(plaintext, ciphertext, counter)
		counter += c.sectorSize / SectorSize // if iv_large_sectors flag set then it should be counter+=1
	}

	return int(length), nil
}

func (c cryptVolume) WriteAt(buf []byte, off int64) (int, error) {
	length := uint64(len(buf))
	offset := uint64(off)

	if length%c.sectorSize != 0 {
		return 0, fmt.Errorf("size of the buffer must be multiple of CryptTable.SectorSize")
	}
	if offset%c.sectorSize != 0 {
		return 0, fmt.Errorf("offset must be multiple of CryptTable.SectorSize")
	}

	sectorSize := int(c.sectorSize)
	counter := uint64(off) / uint64(SectorSize)
	cryptBuf := make([]byte, length)
	for i := 0; i < int(length); i += sectorSize {
		ciphertext := cryptBuf[i : i+sectorSize]
		plaintext := buf[i : i+sectorSize]
		c.cipher.Encrypt(ciphertext, plaintext, counter)
		counter += c.sectorSize / SectorSize // if iv_large_sectors flag set then it should be counter+=1
	}

	return c.f.WriteAt(cryptBuf, off+int64(c.offset))
}

func (c cryptVolume) Close() error {
	return c.f.Close()
}
