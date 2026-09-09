# Karar Defteri

Kural: kritik bilgi 2 kaynaktan doğrulanır. Değişiklik = ölçüm + y/n onayı.

## Kilitli kararlar

- SNI/dest: HER ZAMAN SADECE `whatsapp.net` (`dest=whatsapp.net:443`, serverNames=[whatsapp.net]). Başka domain/alt domain ve manuel/otomatik dest fallback YOK (2026-09-06 kullanıcı kararı). Hedef erişilemezse başka hedefe geçilmez; bağlantı kesintisi kabul edilen risk.
- Protokol: VLESS + Reality + `xtls-rprx-vision`, `:443`, Xray v26.3.27 tabanlı fork `400d51d` (trim + log-seviye kapısı).
- Client: stock Hiddify 3 OS'ta (tek tip). TUN system + Strict route + Bypass LAN OFF + IPv6 route Enable.
- %100 TUN, bypass yok. Tek ortak UUID (3 cihaz).
- In v4-only, out dual v4/v6.
- Fork: CANLIDA `400d51d` (2026-09-06: çalışan binary sürümü + fork commit zinciri doğrulandı; çalışan/disk binary hash'leri aynı). Stock v26.3.27 geri dönüş yedeği.

## Uygulanan tuning (hepsi kalıcı + doğrulandı)

| # | İş | Dosya/yer | Etki |
|---|---|---|---|
| 1 | BBR + fq + TFO=3 + ssi0 | /etc/sysctl.d/99-xray-bbr.conf | bulk 3.6–8x |
| 2 | eth0 fq (replace) | tc (default_qdisc yeni iface'e işlememiş) | BBR eşleşmesi |
| 3 | Live TFO sockopt | /usr/local/etc/xray/config.json | handshake |
| 4 | Log warning + logrotate | config + /etc/logrotate.d/xray | disk hijyeni |
| 5 | GOGC=20 | /etc/systemd/system/xray.service.d/20-gogc.conf | ~3MB RAM (tarihsel; superseded: canlı GOGC=1000, 2026-09-08) |
| 6 | syn_backlog 2048 | 99-xray-bbr.conf (somaxconn zaten 4096) | tarama dayanımı |
| 7 | ~~443 hashlimit~~ KALDIRILDI (2026-09-05) — mobil CGNAT'lı telefonu engelledi, server zaten boşta olduğu için koruma gereksizdi |
| 8 | demir+key+sudo, root kapalı | sshd_config.d/99-no-root.conf | hardening |

## Reddedilenler

- Yaprak qdisc farkı (ölçüldü: yok) • keepalive (ölçüldü: conn 20sn'de kapanıyor)
- GOGC50 (fark gürültüde) • dev buffer'lar (körlemesine yok)

## Bilinen riskler / açık işler

- Reboot persistence: TEST EDİLDİ ✓ (2026-09-05) — sysctl+iptables+xray+GOGC+loglevel hepsi geri geldi,
  eth0 boot'ta otomatik fq aldı. Sim rig (netns/test prosesleri) kalıcı değil, gerekirse SIM.md ile kurulur.
- Kilit profili (2026-09-06, 500-akış): mutex toplam 17ms (tek-seferlik x509 init), block %92 idle-park. Çekişme YOK. dev-prof branch merge edilmedi, sadece lab.
- Latency avı (2026-09-06, lab): TCP_NODELAY yaması ETKİSİZ (10KB/handshake/bulk aynı) — merge
  edilmedi, branch silindi. autocorking=0 + no_metrics_save=1 ETKİSİZ — geri alındı. Kalan ~200ms
  (80ms-RTT sim'de) delayed-ACK yığını; kernelde delack_min yok, per-socket QUICKACK invaziv —
  bırakıldı. Gerçek-hat RTT'si (10–30ms) ile orantılı küçülür, sorun değil.
- Telefonda VPN bağlantısı ve WhatsApp mesajlaşması kullanıcı tarafından doğrulandı (2026-09-06). Gerçek hat performans ölçümü ve Windows testi doğrulanmadı (sim ≠ ISP). Sesli/görüntülü arama test edilmedi; kullanıcı arama testi yapmayacak, bekleyen iş değil.
- ~~Aynı ev-NAT'ından 10 ani handshake → hashlimit geciktirir~~ GEÇERSİZ (2026-09-06: hashlimit 2026-09-05'te kaldırılmıştı — CGNAT'lı telefonu engellemişti; aktif v4+v6 + kalıcı kurallarda canlı doğrulandı, yok).
- Upload tek-akış ~2.2Mbit (çok-akışta 9.4Mbit) — arama için yeterli, dev upload yavaş.
- /tmp tmpfs 484MB — büyük işler /var/tmp'ye.
- Canlı secret'lar: /root/xray-meta.env + /root/xray-privkey (600). vless linki ~/vless-link.txt (Fedora).

## Maksimum throughput avı (2026-09-06, lab, DÜZELTME içerir)

- DOĞRU tavan (loopback, düzgün zamanlama): aggregate **~190MB/s (1.5Gbit)**,
  1-akış 227MB/s > 8-akış toplamı. CPU: srv %28 + cli %29 (tek çekirdek).
- Sınırlayıcı: tek-çekirdek userspace işleme (TLS+Vision framing, iki uçta).
  GOMAXPROCS=1 (nproc=1) — paralelleşme yok, akış artınca verim DÜŞÜYOR.
- DÜZELTME: önceki "10Gbit/0.25sn" sayısı subshell-zamanlama artefaktıydı
  (`( cmd & )` + wait patterni). Geçersiz, yerine bu sayı geçer.
- Pratik hüküm: gerçek-hat/sim hiçbir zaman bu tavana ulaşamaz (ağ önce kapsar:
  sim'de 12Mbit, gerçek mobilde ~500Mbit). 3 kullanıcı için headroom devasa.
  Yapılacak tuning YOK.

## Splice bulgusu (2026-09-06, lab, strace kanıtlı)

- Vision zero-copy (splice) SADECE iç-trafik TLS ise devreye giriyor
  (proxy.go: direct-copy kapısı `IsTLS && TlsApplicationDataStart`).
- HTTP iç-trafik: userspace kopya, tavan ~185MB/s, CPU %25+.
- HTTPS iç-trafik: splice (3927 çağrı/ölçüm), **306MB/s @ %11 CPU**.
- Gerçek kullanıcı trafiği (WhatsApp/HTTPS) zaten splice yolunda — ek ayar YOK,
  latency maliyeti YOK. Önceki 190MB/s tavanı non-TLS yolunmuş.

## 1GB/s sorusunun cevabı (2026-09-06, lab)

- Loopback tavan ~300-350MB/s (tek-conn 347, 4-conn agg 309). BBR/CUBIC fark etmez.
- Sınırlayıcı: TOPLAM CPU %100 (tek çekirdek; srv+cli+python+curl+softirq toplamı).
  Tek prosese bakınca düşük görünüyordu (%12), yanılgıydı — kutunun tamamı dolu.
- Daha fazlası = daha çok çekirdek (büyük VPS) veya bayt-başı daha az iş.
  İkisi de latency'yi ilgilendirmez (farklı eksen).
- Pratik not: gerçek-hat en hızlı ölçüm ~500Mbit (60MB/s) — tavanın 5'te 1'i.
  Bu tavan hiç dokunulmayacak.

## IsCompleteRecord rewrite (2026-09-06): MERGE YOK

- Sebep (alloc profili): akış-başı padding kontrolü akış-başı 18KB kopya.
- Bench 16KB: eski 16.5µs/78KB/18alloc ↔ yeni 35µs/60KB/17alloc.
  Bench gerçekçi-küçük (~517B, 3 parça, ×3 tekrar): eski ~4.1µs/26KB/12alloc ↔
  yeni ~4.4-5.4µs/25.4KB/11alloc. Fark: akış-başı 1 alloc (~600B) + 0.5µs.
  Server A/B'ye gerek görülmedi (ölçülemez mertebe).
- Hüküm: alloc azaldı ama ns/op 2x kötüleşti; ikisi de akış-başına bir kez koşuyor,
  toplam etkinin %6'sı. Karmaşıklığa değmez. Branch `perf-isc` referans duruyor.
  Nil-buffer'da orijinal panic'liyordu (testle kanıtlı), yeni sürüm dayanıklı.

## Log-seviye kapısı (2026-09-06): MERGE EDİLDİ ✓

- Bulgu: errors.doLog seviyeye bakmadan Caller+alloc yapıyordu (filtre downstream'deydi).
- Yama: common/log'da atomik global seviye + 4 fonksiyonda erken dönüş; app/log
  start'ta config'den beslenir. Davranış paritesi çift-yönlü doğrulandı.
- A/B (sim): 10KB/bulk aynı; 100-akış fırtına base 39.6s → **34.7s (%12)**.
- trimmed'a merge + push (CI koşuyor).

## Rollout log-kapılı build (2026-09-06): CANLIDA ✓

- Binary 400d51d (CI artifact, sha doğrulamalı). Yedek: xray.trimmed-prev (+stock).
- Forward doğrulandı. Rollback: stop + geri kopyala + start.

## A/B/C turu sonucu (2026-09-06): değişiklik YOK

- A GOGC 20vs100 fırtına: 34.6 / 37.1 / 39.4 (A-B-A) — gürültü, kazanan yok. 20 kalıyor.
- B bytespool: tier'lar buf.Size ile hizalı (8K tam), 16KB→32K firesi hacimde önemsiz.
- C Reality resume: upstream'de yok + "not planned" kapatılmış; client desteği de yok.
  shortId zaten optimal (16-hex). Yapılacak iş yok.

## Uplink splice deneyi (2026-09-06): KAPATILDI, merge yok

- Upstream TODO'su açıldı (2 flag), build + doğruluk OK (md5 tuttu).
- A/B upload: baz 51MB/s = deney 55MB/s (gürültü). strace: SIFIR splice çağrısı —
  tek flag yetmiyor, uplink direct-copy state machine'i yok.
- Derin cerrahi = upstream risk alanı; upload zaten 424Mbit+. Branch silindi.

## Derin CPU profili (2026-09-06): işlem yok

- 25sn yük-altı CPU profili: %52 syscall I/O, %19 AES-GCM (donanım), kalan handshake gürültüsü.
- Cipher şüphesi çürütüldü: Reality HW-aware seçim yapıyor (AES-NI varsa AES-GCM).
- Kilit/mutex/block profilleri de temizdi. Kod-içi darboğaz resmen YOK.

## Server: swap + THP (2026-09-06)

- Swap yoktu (OOM'da sshd ölmüştü) → 2GB swapfile + fstab + swappiness=10.
- THP always→madvise (runtime + systemd unit). Compaction stall'ları gider.
- Reddedilen: yeni çekirdek (risk/fayda kötü), mitigations=off (komşuya anahtar sızıntısı).

## sshd şifre-auth kapatıldı (2026-09-06)

- cloud-init (50) + Netlen (99) drop-in'leri `yes` ile eziyordu. İkisi de `no` yapıldı,
  efektif `sshd -T` + key-giriş testi doğrulandı.

## sshd root politikası düzeltildi (2026-09-07)

- `99-netlen.conf:1` içindeki `PermitRootLogin yes`, ilk değer kazanır kuralıyla `99-no-root.conf`'u etkisiz bırakıyordu; yalnızca bu değer `no` yapıldı. Öncesinde de `AllowUsers demir` root girişini engelliyordu.
- Yerel yedek: `~/backups/xray/ssh-root-20260907T010904Z/`. `sshd -t`, efektif root/demir politikası ve reload sonrası bağımsız yeni SSH + sudo doğrulandı; Xray PID değişmedi.

## Access-log off + fd-limit (2026-09-06)

- Access log kapatıldı (kimse okumuyordu), fd soft limit 1024→65535.

## 10-madde server hijyeni (2026-09-06)

1. Lab dinleyiciler kapatıldı ✓ (gerekirse SIM.md ile kurulur)
2. LLMNR kapatıldı ✓ (resolved drop-in)
3. X11Forwarding no ✓ | 4. accept_redirects=0 ✓
5. /var/tmp 520MB test çöpü silindi ✓ | 6. journal cap 100M ✓ (49M mevcut kaldı, sorun değil)
7. Çekirdek: 2 sürüm BİLEREK duruyor (fallback); 2026-09-06 kontrollü reboot tamamlandı. Yeni boot ID + `uname -r`: `6.12.107+deb13-cloud-amd64`; SSH, Xray `400d51d`, :443 ve BBR/fq doğrulandı, başarısız systemd birimi yok. Reboot sonrası kullanıcı telefonda VPN bağlantısını ve WhatsApp mesajlaşmasını doğruladı. Sesli/görüntülü arama test edilmedi; kullanıcı arama testi yapmayacak.
8. core_pattern=/dev/null ✓ | 9. Otomatik reboot KAPALI kalacak (`Automatic-Reboot=false`); güncelleme sonrası reboot yalnızca kullanıcının açık onayıyla yapılır (2026-09-06 kullanıcı kararı).
10. Yerel yedek alındı: `~/backups/xray/20260906T184036Z/` (şifresiz, Git dışında; dizin 700/dosya 600). Config+anahtarlar, sysctl, servis, firewall ve 3 binary; arşiv/hash kontrolleri geçti. Tam sunucu imajı değil, restore denenmedi; düzenli yedek politikası açık.

## 20-madde turu uygulamaları (2026-09-06)

- Firewall: default-deny (22+443, v4+v6, kalıcı), yeni SSH + live doğrulandı.
- timestamps=0, kptr_restrict=1, apt AutocleanInterval=7, GRUB 1sn, MOTD kapalı.
- needrestart zaten kuruluymuş. Atlananlar (bilinçli): per-device UUID, Restart=always,
  Watchdog, SSH-port, shortId-rotasyon, policy-timeout, chrony (hepsi kayıtta, gerekçeli).

## buf32 A/B (dürüst sonuç) + firewall dersi (2026-09-06)

- İlk "buf32 asılıyor" bulgusu YANLIŞTI: kendi firewall'ım lab portlarını kesmiş
  (SYN tcpdump'ta görülüp cevapsız kalıyordu). Kural eklenince buf32 çalıştı.
- Adil A/B: 8K ≈ 32K (bulk medyan ~1.17sn ikisi de, 10KB ~0.285sn, CPU ~sıfır).
  MERGE YOK (fayda yok). Branch duruyor.
- Ders: lab portları FW'de allow'lu (10.200.0.0/30); yeni test portu açılınca
  kontrol edilecek ilk yer firewall.

## Production (2026-09-06): SADECE stable

- Lab komple söküldü (unitler, helperlar, sim, /tmp + /var/tmp artıkları).
- 2026-09-07: bağımlılığı kalmayan `filter/FW -s 10.200.0.0/30 -j ACCEPT` kuralı canlı ve kalıcı v4 kurallarından kaldırıldı. Yerel yedek: `~/backups/xray/firewall-lab-20260907T012004Z/`; fark yalnızca bu kural, v6 değişmedi, `iptables-restore --test` ve bağımsız yeni SSH+sudo geçti; Xray PID aynı.
- Dinleyen: :443 live + :22 + local DNS. Yedekler duruyor (stock + trimmed-prev).

## maintain.sh — manuel bakım (2026-09-07: ilk canlı çalıştırma başarılı)

- Düzeltme turu (2026-09-06, canlı yok): remote bloklar `set -euo pipefail`, hata gizleyen pipe/marker kalıpları kaldırıldı (dosya-başı get, entry+hash doğrulaması, apt-config dump ile efektif politika teyidi, `:443`→xray sahipliği, bbr tam-eşleşme, fq-root, çalışan/disk/yedek hash üçlüsü). Test: `bash tests/maintain-smoke.sh` (16 offline senaryo, fake-ssh remote bloğu gerçekten çalıştırır). Bu turda canlı çalıştırılmadı; ilk canlı sonuç aşağıda. Restore DENENMEDİ.
- Düzeltme turu 2 (2026-09-06, canlı yok): busy ps/fuser hataları fail-closed (idle gibi davranmaz), recheck onayın SONRASINA alındı, remote `bash -c` zorunlu (quoting %q), yedek dizin-bazlı (/usr/local/etc/xray, apt.conf.d, sshd_config.d, xray.service.d; üye+dizin doğrulamalı), unattended log atıldı. Test 20 senaryo + mutasyon kontrolü (onay kapısı sökülünce T5 FAIL verir).

- Kullanım (yerel PC'den, manuel): `./maintain.sh <ssh-dest>` (örn. `./maintain.sh demir@<sunucu>`). IP/kimlik repoya yazılmaz; strict host verification; `sudo -n` yoksa fail-fast. Test: `bash tests/maintain-smoke.sh` (offline fake-ssh).
- Politika: otomatik APT güncellemesi KAPALI (2026-09-07: apt-daily/apt-daily-upgrade timerları disabled+inactive; `99-disable-auto-upgrades` ile dört Periodic alanı efektif 0 doğrulandı). `apt-get update` ve `upgrade` yalnızca açık onayla, simülasyon gösterilerek; `-y/full-upgrade/autoremove` YOK, configler korunur (`--force-confold`), restart uyarısı var. Timer/cron kurulmaz, otomatik reboot YOK.
- İlk canlı koşu (2026-09-07): yalnızca yedek + otomatik APT kapatma; `apt-get update` reddedildi, paket kurulumu/reboot yapılmadı. `~/backups/xray/20260907T005504Z/` (26M) COMPLETE, arşiv/hash/700-600 izin kontrolleri geçti. Xray `400d51d`, :443 sahipliği, BBR/fq ve kernel `.107` sağlıklı. Otomatik reboot efektif unset/default false; shutdown helper değiştirilmedi.
- Yerel yedek: `~/backups/xray/<TS>Z/` (Git dışı, şifresiz, 700/600, umask 077). Kapsam: config+anahtarlar, sysctl, unit/drop-in, SSH drop-in, firewall (kalıcı+canlı), 3 binary, APT politikası. Sınırlama: tam imaj DEĞİL, restore DENENMEDİ; kısmi yedek INCOMPLETE işaretli kalır, hata sonrası ilerleme yok.

## Fork subtree importu (2026-09-07)

- Trimli fork `fork/xray-core` altına `git subtree add --no-squash` ile alındı
  (kaynak `https://github.com/DemirSe/xray-optimized-core.git` @ `400d51d…`, merge
  `87f2588b`; `HEAD:fork/xray-core` ağacı kaynak ağaçla eşit `98747e5…`).
  Kilitli kararlar değişmedi: Debian amd64 1vCPU/1GB, stock Hiddify TUN, VLESS
  REALITY Vision, dest yalnızca whatsapp.net, config uyumluluğu, stock rollback.
  Canlı kurulum bu importla yeniden doğrulanmış sayılmaz; sonraki geliştirme adımı
  fork-içi çalışmadır.

## Performans testi sözleşmesi (2026-09-07)

- Kullanıcı kararı: her karşılaştırma ABABABAB (8 tur/4 çift); stock Xray client ve custom server aynı VPS'te, gerçek REALITY dest/SNI daima whatsapp.net. Aynı-host kaynak paylaşımı sonuçlarda belirtilir.
- Gecikme kazancı download/upload kapasitesi pahasına kabul edilmez; tek değişken, eşdeğer iş yükü ve tur bazlı ham veri gerekir. Kalıcı değişiklik ayrıca onay ister.
- İlk GOGC 20/100 karşılaştırması benimsemeyi gerektiren kazanç göstermedi; canlı GOGC=20 korundu. Hız eşdeğerliği ve GC kaynaklı darboğaz kanıtlanmış değildir.

## Canlı GOGC=1000 geçişi (2026-09-08)

- 10-client ABABABAB serisi: 20→100 tutarlı ~%2 gecikme kazancı; 200 ve 1000'de ek kazanç yok, yalnızca RAM artışı (100: ~26MB, 1000: ~36MB heap). Sahip kararı: RAM bol (~700MB boş), GC neredeyse dursun diye 1000 seçildi.
- Uygulama: `20-gogc.conf` → `Environment=GOGC=1000`, yedek `/root/20-gogc.conf.bak-20260908`, daemon-reload + restart; doğrulandı: active, PID 155232, `:443` yalnızca xray, binary `400d51d`. Geri dönüş: yedeği geri yaz + restart.

## CPU dökümü denemesi durdu (2026-09-08)

- 10-client process-bazlı CPU ölçümü iki kez üst-üste ebeveyn zaman aşımına takıldı; ikinci seferde kalan lab (10 kural, netns, test process'leri) elle temizlendi, canlı etkilenmedi (PID 155232, `:443` xray-only). Tekrar denemeden önce harness kurulum süresi kısaltılmalı.

## Profil denemesi: perf bu kutuda ölü (2026-09-08)

- `perf_event_paranoid=3`, sanal PMU yok: 20 sn kayıtta sıfır örnek. Go pprof endpoint'i canlı binary'de yok. %2,5→%2 avı için önce lab-only profilli binary gerekir; şu an park edildi. Lab tamamen temizlendi, canlı etkilenmedi.
