# xray-optimized — Server Tweaking Raporu (2026-09-05)

Stack: Debian 13 (1vCPU/1GB, edge-test) | stock Xray v26.3.27 | VLESS+Reality+Vision :443
SNI/dest: whatsapp.net (TLS1.3+h2+cert `tls ping`+openssl ile doğrulandı, sadece bu dest, fallback yok)
Client: stock Hiddify, %100 TUN, tek ortak UUID. Fork ertelendi.

Test zemini: server-içi netns+veth+netem simülasyonu (istemci hattı denklem dışı).
Profilller (çift-yön): hafif 20ms/%0.2/500Mbit, std 40ms/%1/200Mbit, sert 100ms/%3/50Mbit.

## Uygulananlar

1. **BBR + fq + TFO=3 + slow_start_after_idle=0** (`/etc/sysctl.d/99-xray-bbr.conf`, kalıcı)
   - std profil 2MB medyan: CUBIC 4.61s → BBR 1.28s (~3.6x, dağılım 3.1–9.5 → 1.05–1.31)
   - sert profil 2MB medyan: CUBIC ~28s → BBR ~3.4s (~8x)
   - 10KB: 0.36s → 0.29s (hafif iyileşme, daha stabil)
   - Canlı :443'teki 8/8 soket `bbr` doğrulandı.
2. **eth0 fq** — sysctl sonrası eth0 eski `fq_codel`'de kalmıştı, `tc replace` ile fq'ya geçti (kesintisiz).
3. **Live TFO sockopt** (`streamSettings.sockopt.tcpFastOpen`) + restart, forward testi temiz.
4. **Log warning + logrotate** — hız farkı yok (ölçüldü), ama debug live'da 4.3MB birikmişti; disk hijyeni.
5. **GOGC=20** (systemd override) — hız/CPU farkı yok, ~3MB RAM (38.1→35.1MB).
6. **syn_backlog 128→2048** — :443'te 356 TIME-WAIT birikimi görülmüştü (tarama churn'ü).
7. **443 hashlimit** (IP-başı 20/dk, burst 8, kalıcı) — rig testinde 40 flood→10 OK/30 DROP,
   legit ~0.28s etkilenmedi. SSH ayrı portta, kilit riski yok.
8. **Hardening** — `demir` kullanıcısı + SSH key + şifresiz sudo, root SSH kapalı, şifre artıkları silindi.

## Reddedilenler (ölçüldü, faydasız)

- **Yaprak qdisc** (pfifo vs fq vs fq_codel, netem altı): fark gürültü bandında. Hız BBR'dan geliyor.
- **Kernel keepalive**: idle Reality conn 20sn içinde kendiliğinden kapanıyor, ölü-conn birikmiyor. Değişiklik yok.
- **GOGC=50**: A/B/C farkı gürültüde, test edilmedi.
- **Dev buffer sysctl'leri**: körlemesine eklenmedi.

## Çoklu-cihaz yük testi (3 client, bulk+web+seyrek, aynı darboğaz)

- mild: 33sn toplam, bulk ~0.9s, web ~0.15s, sparse ~0.15s — tekliyle AYNI, bozulma yok
- std: 37sn toplam, bulk ~2.2s (tekli 1.28), web ~0.3s, sparse ~0.3s + nadir 0.8–1.4s outlier
- Tüm koşularda 0 curl hatası. Server CPU yükte ~%1.5 (1vCPU), RSS ~38MB.
- Hüküm: tuning'ler 3 cihazda tutarlı, açlık/starvation yok. Yeni tuning ihtiyacı YOK.
- İtiraf: ilk kötü multi sonuçları test artefaktıydı — rig portundaki hashlimit (XRAYT)
  3 client'ın ortak IP'sini boğmuş (698 DROP). Kaldırılıp baştan ölçüldü. Live :443 kuralı
  gerçekte cihaz-başı IP ile çalışır, sorun yok.

## Detay batch (UDP / upload / büyük dosya / fırtına / 10-conn)

- **UDP (VoIP sim, 50pps×60sn):** 3000/3000, %0 kayıp, RTT med 93ms / p95 169ms (sim bazı ~80ms)
- **Upload tek-akış:** ~2.2Mbit (download ~12Mbit'in altında); 4 akışta 9.4Mbit — akış-başı
  BBR dinamiği, config sorunu değil. Arama için fazlasıyla yeter (100kbit–2Mbit).
- **500MB:** bütünlük OK (md5 eşleşti), 32.7s, kesinti yok
- **Handshake fırtınası:** 10/10 fresh Reality handshake ~0.3–0.8s, firesiz
- **10×2MB eşzamanlı:** hepsi 1.3–2.4s, 0 hata
- Not: live `:443` hashlimit aynı-IP'den 10 ani handshake'in fazlasını geciktirir;
  farklı IP'lerde sorun yok, aynı ev-NAT'ında aynı-anda reconnect'te retry ile düzelir.

## Notlar

- Ara 2.3s yavaşlama bölümü tekrarlanamadı → kirli restart artığıydı, BBR değil (çift-kontrol edildi).
  Ders: test prosesleri hep systemd-unit ile.
- Sim ISP DPI/throttle karakterini vermez; gerçek-hat testi ayrıca gerekli.
- Rig (std profil + BBR, :1443 + netns client) gelecek A/B'ler için hazır duruyor.
- Ölçüm ham verisi server'da: /tmp/tbench/matrix.txt
