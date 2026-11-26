# Panduan Rilis, GitHub Releases, dan Repositori APT Ubuntu/Debian

Dokumen ini menjelaskan langkah lengkap untuk mem-publish `ez-ssh` ke GitHub (`git@github.com:ib-jpg/ez-ssh.git`), mengaktifkan repositori APT agar pengguna Ubuntu/Debian bisa menjalankan `sudo apt install ez-ssh`, serta alur update ke versi baru seperti `1.0.2`, hingga jalur resmi Launchpad PPA dan repositori bawaan Debian/Ubuntu Universe.

---

## 1. Push Pertama ke GitHub (`ib-jpg/ez-ssh`)

Repository lokal sudah dikonfigurasi dengan `origin` ke `git@github.com:ib-jpg/ez-ssh.git`. Karena kamu sebelumnya sudah membuat file `LICENSE` di GitHub, kemungkinan ada 1 commit awal di branch `main` remote.

1. Pastikan SSH key kamu sudah terdaftar di GitHub:
   ```bash
   ssh -T git@github.com
   ```
2. Commit semua file proyek di lokal:
   ```bash
   git add .
   git commit -m "feat: prepare ez-ssh v1.0.0 for GitHub and multi-distro packaging"
   ```
3. Sinkronkan dengan commit awal GitHub (jika ada `LICENSE` dari UI GitHub), lalu push:
   ```bash
   git pull --rebase origin main --allow-unrelated-histories
   git push -u origin main
   ```
   *(Jika saat `pull --rebase` muncul konflik di `LICENSE`, pilih versi lokal yang sama isinya dengan `git checkout --ours LICENSE && git add LICENSE && git rebase --continue`.)*

---

## 2. Setup Sekali Saja: Repositori APT Ubuntu/Debian via GitHub Pages

Workflow [release.yml](../.github/workflows/release.yml) akan otomatis membuat dan menandatangani repositori APT di branch `gh-pages` setiap kali tag `v*.*.*` di-push—asalkan secret GPG berikut sudah dipasang di GitHub.

### A. Buat GPG Key Khusus Signing APT
Di komputer lokal:
```bash
gpg --full-generate-key
```
Pilih:
- Jenis key: `(1) RSA and RSA` (atau ECC default)
- Panjang bit: `4096`
- Masa berlaku: misalnya `2y` atau `0` (tidak kadaluarsa)
- Nama & Email: `Ibrahim Irsyad <ibrairsyd0406@gmail.com>`

Lihat Key ID yang baru dibuat:
```bash
gpg --list-secret-keys --keyid-format=long
```
Salin ID setelah `rsa4096/` (misalnya `ABCDEF1234567890`), lalu export private key-nya:
```bash
gpg --armor --export-secret-keys ABCDEF1234567890
```

### B. Simpan Secret di Repository GitHub
Buka **GitHub → `ib-jpg/ez-ssh` → Settings → Secrets and variables → Actions → New repository secret**:
1. **`GPG_PRIVATE_KEY`**: tempel seluruh output `-----BEGIN PGP PRIVATE KEY BLOCK----- ... -----END PGP PRIVATE KEY BLOCK-----`.
2. **`GPG_PASSPHRASE`** *(opsional)*: isi jika saat membuat GPG key tadi kamu memakai passphrase; biarkan kosong jika tidak memakai passphrase.

*(Catatan: Jika `GPG_PRIVATE_KEY` belum diisi, workflow `release.yml` tetap sukses merilis `.deb`, `.rpm`, `.apk`, `.pkg.tar.zst`, dan `.tar.gz` ke GitHub Releases, dan hanya melewati publikasi `gh-pages` APT sampai secret ditambahkan.)*

### C. Aktifkan Izin Actions & GitHub Pages
1. Di **Settings → Actions → General → Workflow permissions**, pilih **Read and write permissions**.
2. Setelah rilis pertama membuat branch `gh-pages`, buka **Settings → Pages**:
   - **Source**: `Deploy from a branch`
   - **Branch**: `gh-pages` / `/ (root)`

Setelah aktif, pengguna Ubuntu/Debian di mana saja dapat meng-install dengan:
```bash
curl -fsSL https://ib-jpg.github.io/ez-ssh/apt/gpg.key | sudo gpg --dearmor -o /usr/share/keyrings/ez-ssh-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/ez-ssh-archive-keyring.gpg] https://ib-jpg.github.io/ez-ssh/apt stable main" | sudo tee /etc/apt/sources.list.d/ez-ssh.list
sudo apt update && sudo apt install ez-ssh
```

---

## 3. Cara Merilis & Meng-update ke Versi Baru (Contoh: `1.0.2`)

Ketika kamu selesai mengubah kode atau memperbaiki bug dan ingin merilis versi `1.0.2`:

1. Update informasi versi pada file sumber:
   - [CHANGELOG.md](../CHANGELOG.md): tambahkan section `## [1.0.2] - YYYY-MM-DD`.
   - [debian/changelog](../debian/changelog): tambahkan entri baru di paling atas dengan format Debian (misalnya `ez-ssh (1.0.2-1) unstable; urgency=medium`).
   - [cmd/ez-ssh/main.go](../cmd/ez-ssh/main.go): ubah fallback `var version = "1.0.2"` (saat rilis via GoReleaser, nilai ini juga otomatis di-inject lewat `-ldflags -X main.version={{.Version}}`).
2. Pastikan semua test lulus:
   ```bash
   go vet ./...
   go test ./...
   ```
3. Commit perubahan, buat tag SemVer, dan push ke GitHub:
   ```bash
   git add .
   git commit -m "chore: release v1.0.2"
   git tag -a v1.0.2 -m "Release v1.0.2"
   git push origin main --tags
   ```

### Apa yang terjadi otomatis di GitHub?
- GitHub Actions menjalankan [release.yml](../.github/workflows/release.yml) dengan [GoReleaser](../.goreleaser.yaml):
  - Membangun binary statis untuk `linux` (`amd64`, `arm64`, `386`) dan `darwin` (`amd64`, `arm64`).
  - Memaketkan `.deb` (Ubuntu/Debian), `.rpm` (Fedora/RHEL/openSUSE), `.apk` (Alpine), `.pkg.tar.zst` (Arch Linux), `.tar.gz`, dan `checksums.txt` ke halaman **GitHub Releases**.
  - Job `publish-apt` menyalin `.deb` versi `1.0.2` ke `apt/pool/main/`, memperbarui indeks `Packages` / `Packages.gz` / `Release`, menandatangani `InRelease` & `Release.gpg` dengan GPG key kamu, dan push ke branch `gh-pages`.
- Di sisi pengguna Ubuntu/Debian yang sudah memasang sumber APT repo kamu, update menjadi `1.0.2` cukup dengan perintah standar:
  ```bash
  sudo apt update && sudo apt upgrade
  ```
- Di distro non-Debian, pengguna tinggal menjalankan ulang skrip [install-release.sh](../scripts/install-release.sh), `go install github.com/ib-jpg/ez-ssh/cmd/ez-ssh@v1.0.2`, atau memasang paket `.rpm` / `.apk` / `.pkg.tar.zst` dari GitHub Releases.

---

## 4. Jalur Resmi Launchpad PPA & Repositori Bawaan Ubuntu/Debian

### A. Launchpad PPA (Khusus Ubuntu, `sudo add-apt-repository ppa:...`)
Jika ingin nama repo `ppa:username/ez-ssh`:
1. Buat akun di [launchpad.net](https://launchpad.net) dan daftarkan GPG key + SSH key kamu.
2. Buat PPA baru (misal `ez-ssh`).
3. Direktori [debian/](../debian/) di repo ini sudah siap dipakai untuk source package Ubuntu (`3.0 (quilt)`, `control`, `rules`, `copyright`, `changelog`, `manpages`).
   - Pastikan seluruh dependency Go tersedia offline saat build di Launchpad (`vendor/` disertakan dalam source tarball upload PPA karena build farm Launchpad memblokir akses internet selama `debuild`). `debian/rules` otomatis memakai `-mod=vendor` jika folder `vendor/` ada.
4. Bangun dan unggah source package:
   ```bash
   debuild -S -sa
   dput ppa:<launchpad-user>/ez-ssh ../ez-ssh_1.0.0-1_source.changes
   ```
   Untuk update ke `1.0.2`, cukup naikkan `debian/changelog` ke `1.0.2-1~ubuntu24.04.1`, lalu ulangi `debuild -S` dan `dput`.

### B. Masuk ke Repositori Bawaan Resmi (`sudo apt install ez-ssh` Tanpa Menambah Repo Luar)
Agar `ez-ssh` langsung tersedia di repositori resmi **Debian Main** dan otomatis tersinkron ke **Ubuntu Universe** tanpa perlu `apt` repo tambahan maupun PPA:
1. **Kebijakan Debian Go Packaging Team**: Debian/Ubuntu resmi **tidak mengizinkan vendoring** (`vendor/`). Seluruh library Go di `go.mod` harus sudah tersedia sebagai paket Debian `golang-*-dev` di arsip Debian, atau dikemas satu per satu terlebih dahulu.
2. **Ajukan ITP (*Intent To Package*)**: Buka bug report `wnpp` di [bugs.debian.org](https://www.debian.org/devel/wnpp/) bertipe `ITP` untuk paket `ez-ssh`.
3. **Upload ke `mentors.debian.net` & Cari Sponsor (Debian Developer)**: Paket yang lolos standar Lintian dan Debian Policy akan di-upload oleh sponsor Debian Developer ke Debian `unstable` (`sid`), lalu masuk ke `testing`.
4. **Sinkronisasi ke Ubuntu Universe**: Ubuntu secara otomatis menarik paket baru dari Debian `testing` selama fase perkembangan sebelum *Debian Import Freeze* tiap siklus rilis Ubuntu (seperti 26.04 LTS / 26.10).
5. **Alur Update Setelah Masuk Resmi**: Saat kamu merilis `1.0.2` di GitHub, maintainer/sponsor Debian mengunggah `ez-ssh_1.0.2-1` ke Debian; versi baru itu baru akan masuk di rilis Ubuntu berikutnya (atau lewat proses *SRU / Backports* jika untuk perbaikan bug penting di versi LTS yang sudah rilis). Karena itu, **APT repo GitHub Pages + Launchpad PPA** tetap sangat berguna agar pengguna Ubuntu yang ada saat ini langsung mendapat update `1.0.2` secara instan.
