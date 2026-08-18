package torrbalancer

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/ssh"
)

// SSH-управление бэкендом: когда /shutdown не возвращает TorrServer к жизни
// (процесс перезапустился, а торрент-подсистема так и висит — прод 2026-08-13,
// Selectel), остаётся только рестарт средствами самой машины. Креды задаются
// per-backend в админке «TS Балансер»; без них SSH-управление молча недоступно.

const (
	sshDialTimeout = 10 * time.Second
	sshCmdTimeout  = 45 * time.Second
	sshDefaultCmd  = "systemctl restart torrserver"
)

// HasSSH reports whether this backend has enough SSH config to be managed.
func (b *Backend) HasSSH() bool {
	return strings.TrimSpace(b.SSHPassword) != ""
}

// sshAddr derives the dial address: explicit ssh_host, else the host part of
// the backend URL; explicit ssh_port, else 22 (никогда не порт TorrServer'а).
func (b *Backend) sshAddr() (string, error) {
	host := strings.TrimSpace(b.SSHHost)
	if host == "" {
		u, err := url.Parse(b.Host)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("cannot derive ssh host from %q", b.Host)
		}
		host = u.Hostname()
	}
	port := b.SSHPort
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port)), nil
}

// SSHRestartBackend dials the backend's box and runs its restart command
// (default: systemctl restart torrserver). Returns the command's combined
// output. Safe to call from request handlers — hard-bounded by timeouts.
func (p *Pool) SSHRestartBackend(b *Backend) (string, error) {
	if b == nil {
		return "", errors.New("nil backend")
	}
	if !b.HasSSH() {
		return "", errors.New("SSH не настроен для этого бэкенда")
	}
	addr, err := b.sshAddr()
	if err != nil {
		return "", err
	}
	user := strings.TrimSpace(b.SSHUser)
	if user == "" {
		user = "root"
	}
	cmd := strings.TrimSpace(b.SSHCmd)
	if cmd == "" {
		cmd = sshDefaultCmd
	}

	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.Password(b.SSHPassword)},
		// Бэкенды — свои машины, известные по IP из конфига; TOFU-хранилища
		// ключей здесь нет, а рестарт должен работать и после переустановки ОС.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         sshDialTimeout,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer sess.Close()

	// session.CombinedOutput не умеет контекст — ограничиваем время жизни всего
	// клиента: по таймеру рвём соединение, и CombinedOutput возвращается.
	timer := time.AfterFunc(sshCmdTimeout, func() { _ = client.Close() })
	defer timer.Stop()

	out, err := sess.CombinedOutput(cmd)
	text := strings.TrimSpace(string(out))
	if len(text) > 4096 {
		text = text[:4096] + "…"
	}
	log.Info().Str("backend", b.Host).Str("addr", addr).Str("cmd", cmd).Err(err).
		Str("output", text).Msg("torrbalancer: ssh restart executed")
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s: %w", cmd, err)
		}
		return "", fmt.Errorf("%s: %w", cmd, err)
	}
	return text, nil
}
