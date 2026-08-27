# rclone compatibility fixtures

Captured from the real rclone binary v1.73.3 (`rclone --version`):

- `plain/`    deterministic plaintext corpus
- `enc32/`    ciphertext produced by `rclone copy` through a crypt remote
  configured with `password=testpassword` `password2=testsalt`
  (filename_encoding=base32, filename_encryption=standard)
- `enc64/`    same corpus, filename_encoding=base64
- `map32.txt` `encrypted-name <TAB> plaintext-name` for enc32/
- `map64.txt` same for enc64/

Each ciphertext begins with the rclone file header (`RCLONE\x00\x00` magic +
24-byte nonce) followed by secretbox blocks. rclone generates a random nonce
per file, so these ciphertexts are one-shot fixtures: they are frozen here and
must not be regenerated randomly.

Regeneration procedure (documented in tests): create a crypt remote with
`rclone config create c crypt remote=DIR password=testpassword
password2=testsalt`, run `rclone copy PLAIN c:data`, and move the resulting
files from DIR.
