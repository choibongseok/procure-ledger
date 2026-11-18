package backup

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// 암호화 형식(명세 6.2, D3): 파일 전체를 청크(64KiB) 단위 AES-256-GCM 으로 감싼다.
//
//	머리: "PLBK1\n" + 무작위 nonce 접두 8바이트
//	청크: 평문 길이 4바이트(빅엔디언) 없이, 암호문 길이를 앞에 둔다: [4바이트 길이][암호문]
//	nonce = 접두 8바이트 + 청크 번호 4바이트, 추가 인증 데이터(AAD) = 머리 + 마지막 청크 표시 1바이트
//
// 마지막 청크는 AAD 에 표시가 있어 뒤를 잘라낸 파일은 복호화 때 인증에 실패한다(잘림 검출).
const (
	cryptMagic = "PLBK1\n"
	chunkSize  = 64 * 1024
	minKeyLen  = 16
)

// ErrWrongKey 는 키가 틀렸거나 파일이 변조·손상된 경우다(어느 쪽인지는 구분하지 않는다: 내용 노출 없음).
var ErrWrongKey = errors.New("백업 키가 맞지 않거나 파일이 손상되었습니다")

// DeriveKey 는 PL_BACKUP_KEY 문자열에서 AES-256 키를 만든다. 16자 미만이면 거부한다.
func DeriveKey(secret string) ([]byte, error) {
	if len(secret) < minKeyLen {
		return nil, fmt.Errorf("PL_BACKUP_KEY 는 %d자 이상이어야 합니다", minKeyLen)
	}
	sum := sha256.Sum256([]byte("procure-ledger-backup-key|" + secret))
	return sum[:], nil
}

// Fingerprint 는 키 지문(앞 8자)이다. 매니페스트에 적어 틀린 키를 알린다. 키 자체는 복원할 수 없다.
func Fingerprint(key []byte) string {
	sum := sha256.Sum256(append([]byte("procure-ledger-backup-fp|"), key...))
	return hex.EncodeToString(sum[:])[:8]
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func chunkNonce(prefix []byte, n uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[8:], n)
	return nonce
}

func chunkAAD(final bool) []byte {
	if final {
		return []byte(cryptMagic + "\x01")
	}
	return []byte(cryptMagic + "\x00")
}

// EncryptFile 은 src 를 암호화해 dst 에 쓴다(권한 0600).
func EncryptFile(src, dst string, key []byte) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := encryptStream(in, out, key); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func encryptStream(in io.Reader, out io.Writer, key []byte) error {
	g, err := gcmFor(key)
	if err != nil {
		return err
	}
	prefix := make([]byte, 8)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	if _, err := w.WriteString(cryptMagic); err != nil {
		return err
	}
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	rd := bufio.NewReaderSize(in, chunkSize+1)
	buf := make([]byte, chunkSize)
	var n uint32
	for {
		got, rerr := io.ReadFull(rd, buf)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}
		// 다음에 읽을 것이 더 있는지 엿본다: 없으면 이 청크가 마지막이다(빈 파일도 빈 마지막 청크 하나를 쓴다).
		_, perr := rd.Peek(1)
		final := rerr == io.EOF || rerr == io.ErrUnexpectedEOF || perr == io.EOF
		ct := g.Seal(nil, chunkNonce(prefix, n), buf[:got], chunkAAD(final))
		var ln [4]byte
		binary.BigEndian.PutUint32(ln[:], uint32(len(ct)))
		if _, err := w.Write(ln[:]); err != nil {
			return err
		}
		if _, err := w.Write(ct); err != nil {
			return err
		}
		n++
		if final {
			break
		}
	}
	return w.Flush()
}

// DecryptFile 은 src 를 복호화해 dst 에 쓴다(권한 0600). 틀린 키·변조·잘림은 ErrWrongKey 로 돌려주고 dst 를 남기지 않는다.
func DecryptFile(src, dst string, key []byte) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := decryptStream(in, out, key); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func decryptStream(in io.Reader, out io.Writer, key []byte) error {
	g, err := gcmFor(key)
	if err != nil {
		return err
	}
	rd := bufio.NewReader(in)
	head := make([]byte, len(cryptMagic)+8)
	if _, err := io.ReadFull(rd, head); err != nil || string(head[:len(cryptMagic)]) != cryptMagic {
		return ErrWrongKey
	}
	prefix := head[len(cryptMagic):]
	w := bufio.NewWriter(out)
	var n uint32
	for {
		var ln [4]byte
		if _, err := io.ReadFull(rd, ln[:]); err != nil {
			return ErrWrongKey // 마지막 청크 없이 끝남(잘림)
		}
		size := binary.BigEndian.Uint32(ln[:])
		if size < uint32(g.Overhead()) || size > chunkSize+uint32(g.Overhead()) {
			return ErrWrongKey
		}
		ct := make([]byte, size)
		if _, err := io.ReadFull(rd, ct); err != nil {
			return ErrWrongKey
		}
		_, perr := rd.Peek(1)
		final := perr == io.EOF
		pt, err := g.Open(nil, chunkNonce(prefix, n), ct, chunkAAD(final))
		if err != nil {
			return ErrWrongKey
		}
		if _, err := w.Write(pt); err != nil {
			return err
		}
		n++
		if final {
			break
		}
	}
	return w.Flush()
}
