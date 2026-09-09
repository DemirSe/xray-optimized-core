# Simülasyon & Test Rig Rehberi

Amaç: istemci hattı stabilize olmadığı için TÜM ölçümler server-içi simülasyonda.
Server: edge-test (158.94.216.102), Debian 13, BBR+fq+TFO3. Ham veri: /tmp/tbench/matrix.txt

## 1. Sim hattı (netns + veth + netem)

```bash
sudo ip netns add testwan
sudo ip link add veth-host type veth peer name veth-ns
sudo ip link set veth-ns netns testwan
sudo ip addr add 10.200.0.1/30 dev veth-host; sudo ip link set veth-host up
sudo ip netns exec testwan bash -c "ip addr add 10.200.0.2/30 dev veth-ns; ip link set veth-ns up; ip link set lo up"
```

Profil uygula (ÇİFT YÖN, ikisine de!):
```bash
# std profil
sudo tc qdisc add dev veth-host root netem delay 40ms 10ms distribution normal loss 1% rate 200mbit
sudo ip netns exec testwan tc qdisc add dev veth-ns root netem delay 40ms 10ms distribution normal loss 1% rate 200mbit
# değiştirmek için: `tc qdisc change ...` (del/add da olur)
```

Profilller: hafif `20ms 5ms loss 0.2% rate 500mbit` | std (yukarı) | sert `100ms 20ms loss 3% rate 50mbit`

Doğrula: `sudo ip netns exec testwan ping -c 10 10.200.0.1` ve iperf3 (systemd ile!):
```bash
sudo systemd-run --unit=iperf-s --collect -p Restart=no /usr/bin/iperf3 -s -p 5201
sudo ip netns exec testwan iperf3 -c 10.200.0.1 -p 5201 -R -t 12   # downlink
sudo ip netns exec testwan iperf3 -c 10.200.0.1 -p 5201 -t 10     # uplink
```

Temizlik: `sudo ip netns del testwan` (veth'ler otomatik gider).

## 2. Tünel rig (canlı :443'e DOKUNMAZ)

- Test inbound: stock xray, `10.200.0.1:1443`, canlı configin kopyası (aynı UUID/keys: /root/xray-meta.env + /root/xray-privkey).
- Config üretimi /tmp/tbench'te (srv-test.json, cli-ns.json). Testten önce her zaman:
  `/usr/local/bin/xray run -test -c <dosya>`
- Prosesler HEP systemd-unit ile (çıplak `&` SSH düşünce ölüyor + yarım proses artefaktı üretiyor!):
```bash
sudo systemd-run --unit=xray-srv-t --collect -p Restart=no /usr/local/bin/xray run -c /tmp/tbench/srv-test.json
sudo systemd-run --unit=xray-cli-ns --collect -p Restart=no /usr/bin/ip netns exec testwan /usr/local/bin/xray run -c /tmp/tbench/cli-ns.json
# env ile: -p Environment=GOGC=20  # canlı GOGC=1000 (2026-09-08); 20 rig A/B değeri, tarihsel.
# durdurma: sudo systemctl stop xray-srv-t xray-cli-ns
```
- Client: socks `127.0.0.1:10800` (ns içinden). Kullanım:
```bash
sudo ip netns exec testwan curl -s -o /dev/null --socks5-hostname 127.0.0.1:10800 -w "%{time_total}s %{speed_download}B/s\n" http://127.0.0.1:8000/f20m.bin --max-time 200
```

## 3. Yardımcı servisler (host)

- HTTP: `python3 -m http.server 8000 --directory /tmp/tbench` (tek-thread!) +
  ThreadingHTTPServer `:8001` (çoklu testlerde BUNU kullan, tek-thread kuyruk artefaktı yapar!)
- Test dosyaları: f10k.bin (10KB), f2m.bin, f20m.bin (/tmp'te).
- 500MB: /var/tmp/tbench-big/f500m.bin (DİKKAT: /tmp tmpfs 484MB! büyük indirmeleri /var/tmp'ye yap).
  Serve: /tmp/tbench/f500m.bin → symlink. md5 referans: d8b61b2c0025
- Upload alıcı: /tmp/tbench/recv.py `:8002` (POST yutucu). Kullanım: `curl --data-binary @dosya http://127.0.0.1:8002/`
- UDP echo: /tmp/tbench/udpecho.py `:9999` + cli'de dokodemo-door UDP `:10900`→9999.
  VoIP sender: /tmp/tbench/voip.py (50pps×60s, select'li ALICI+VERİCİ — tek-yönlü gönderim soket taşırır, %92 sahte kayıp!).
- TCP-upload iperf: cli'ye dokodemo-door TCP `:10901`→127.0.0.1:5201, hostta iperf3 -s.

## 4. Senaryolar

- **Baz:** 5×2MB (medyan) + 20×10KB (medyan). Karşılaştırma hep MEDYAN, alterne sırala.
- **CC A/B:** `sudo /sbin/sysctl -w net.ipv4.tcp_congestion_control=bbr|cubic` + rig restart (yeni soket şart!).
- **Yaprak qdisc A/B:** `tc qdisc add dev veth-host parent 1:1 handle 10: fq|fq_codel` (netem handle 1: altında).
- **Log/GC A/B:** srv-debug.json vs srv-warn.json + `-p Environment=GOGC=20`. (canlı GOGC=1000 (2026-09-08); 20 tarihsel A/B değeri.)
- **Multi (3 cihaz):** cli-10801/2/3.json (3 ayrı Reality conn) + /tmp/tbench/multi.sh
  (bulk 3×20MB + web 30×10KB + seyrek 15×10KB/2sn). Redirect'ler süslü parantez DIŞINA!
- **Storm:** cli-10910..10919 (10 fresh conn) + paralel tek curl. **DİKKAT:** aynı IP'den çıkıyorlar;
  rig portunda hashlimit OLMAYACAK (XRAYT kaldırıldı, sebebi matrix'te).
- **UDP:** voip.py (sonuç referans: %0 kayıp, med 93ms/p95 169ms std profilde).
- **Keepalive-ölüm:** ns'de `iptables -A OUTPUT -d 10.200.0.1 -j DROP` + `ss` ile ESTAB takibi.
  (Bilinen sonuç: idle conn 20sn'de kendiliğinden kapanıyor, keepalive gereksiz.)
- **Hashlimit flood:** `seq 1 40 | xargs -P 10 ... openssl s_client` → OK/DROP say.
- **CPU:** `ps %cpu` ÖLÜDÜR (yaşam-boyu ort.) — /proc/PID/stat 14+15 nolu alan deltası kullan:
  `T1=$(awk '{print $14+$15}' /proc/$SRV/stat); sleep 2; ...` (referans: yükte %1.5/1vCPU, RSS ~38MB).

## 5. Bilinen artefaktlar (tekrar düşme!)

1. Rig portunda hashlimit + aynı-IP client'lar = sahte stall (698 DROP vakası).
2. Çıplak `nohup ... &` SSH komutunda = sessiz ölüm + yarım proses (2.3s hayaleti). Hep systemd-run.
3. Tek-thread http.server çoklu testte kuyruklar. Threaded :8001 kullan.
4. Alıcısız UDP gönderimi = sahte %92 kayıp. select'li voip.py kullan.
5. /tmp tmpfs 484MB — büyük dosyalar /var/tmp'ye.
6. `-w "... exit:$?"` içindeki $? curl'ün değil, önceki komutun kodu. Exit'i ayrı `echo` ile al.
7. Tek-akış sayılarıyla çoklu-akış yorumlama; medyan kullan, outlier'ları not et.
8. `tc`/`sysctl` demir'in PATH'inde yok: `sudo /sbin/tc`, `sudo /sbin/sysctl`.
9. default_qdisc sadece YENİ interface'e işler (eth0'da `tc replace` gerekti).
10. Sysctl CC değişimi mevcut soketleri etkilemez — rig restart şart.
