# Karar Defteri

Kural: kritik bilgi 2 kaynaktan doğrulanır. Değişiklik = ölçüm + y/n onayı.

## Kilitli kararlar

- SNI/dest: SADECE `whatsapp.net` (`dest=whatsapp.net:443`, serverNames=[whatsapp.net]). Fallback YOK.
- Protokol: VLESS + Reality + `xtls-rprx-vision`, `:443`, stock Xray v26.3.27 (stabil pin).
- Client: stock Hiddify 3 OS'ta (tek tip). TUN system + Strict route + Bypass LAN OFF + IPv6 route Enable.
- %100 TUN, bypass yok. Tek ortak UUID (3 cihaz).
- In v4-only, out dual v4/v6.
- Fork: ERTELENDİ (gerekçe: server boşta, stock yetiyor; ihtiyaç ölçümle doğarsa).

## Uygulanan tuning (hepsi kalıcı + doğrulandı)

| # | İş | Dosya/yer | Etki |
|---|---|---|---|
| 1 | BBR + fq + TFO=3 + ssi0 | /etc/sysctl.d/99-xray-bbr.conf | bulk 3.6–8x |
| 2 | eth0 fq (replace) | tc (default_qdisc yeni iface'e işlememiş) | BBR eşleşmesi |
| 3 | Live TFO sockopt | /usr/local/etc/xray/config.json | handshake |
| 4 | Log warning + logrotate | config + /etc/logrotate.d/xray | disk hijyeni |
| 5 | GOGC=20 | /etc/systemd/system/xray.service.d/20-gogc.conf | ~3MB RAM |
| 6 | syn_backlog 2048 | 99-xray-bbr.conf (somaxconn zaten 4096) | tarama dayanımı |
| 7 | ~~443 hashlimit~~ KALDIRILDI (2026-09-05) — mobil CGNAT'lı telefonu engelledi, server zaten boşta olduğu için koruma gereksizdi |
| 8 | demir+key+sudo, root kapalı | sshd_config.d/99-no-root.conf | hardening |

## Reddedilenler

- Yaprak qdisc farkı (ölçüldü: yok) • keepalive (ölçüldü: conn 20sn'de kapanıyor)
- GOGC50 (fark gürültüde) • dev buffer'lar (körlemesine yok) • fork (ertelendi)

## Bilinen riskler / açık işler

- Reboot persistence: TEST EDİLDİ ✓ (2026-09-05) — sysctl+iptables+xray+GOGC+loglevel hepsi geri geldi,
  eth0 boot'ta otomatik fq aldı. Sim rig (netns/test prosesleri) kalıcı değil, gerekirse SIM.md ile kurulur.
- Kilit profili (2026-09-06, 500-akış): mutex toplam 17ms (tek-seferlik x509 init), block %92 idle-park. Çekişme YOK. dev-prof branch merge edilmedi, sadece lab.
- Latency avı (2026-09-06, lab): TCP_NODELAY yaması ETKİSİZ (10KB/handshake/bulk aynı) — merge
  edilmedi, branch silindi. autocorking=0 + no_metrics_save=1 ETKİSİZ — geri alındı. Kalan ~200ms
  (80ms-RTT sim'de) delayed-ACK yığını; kernelde delack_min yok, per-socket QUICKACK invaziv —
  bırakıldı. Gerçek-hat RTT'si (10–30ms) ile orantılı küçülür, sorun değil.
- Gerçek-hat testi yapılmadı (sim ≠ ISP). Bekleyen: kullanıcının Windows/Android testi + WhatsApp sesli/görüntülü.
- Aynı ev-NAT'ından 10 ani handshake → hashlimit geciktirir (retry ile düzelir).
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
