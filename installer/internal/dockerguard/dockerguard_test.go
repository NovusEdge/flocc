package dockerguard

import "testing"

func TestTouchesDocker(t *testing.T) {
	for _, c := range []string{
		"sudo -n systemctl enable --now docker.service",
		"sudo -n systemctl disable --now docker.service",
		"sudo -n systemctl --no-block restart docker",
		"sudo -n systemctl restart --no-block docker.service",
		"sudo -n systemctl try-reload-or-restart docker",
		"sudo -n systemctl reload-or-try-restart docker",
		"sudo -n systemctl force-reload docker",
		"sudo -n systemctl condrestart docker",
		"sudo -n systemctl mask docker.socket",
		"sudo -n /usr/bin/systemctl stop docker.socket",
		"sudo -n systemctl restart flocc-docker-user.service docker.service",
		"sh -c 'iptables -L && systemctl restart docker'",
		"sudo -n /etc/init.d/docker restart",
		"sudo -n service docker restart",
		"sudo -n kill $(pidof dockerd)",
		"sudo -n pkill dockerd",
		"sudo -n pkill -f /usr/bin/dockerd",
		"sudo -n killall dockerd",
		"sudo -n dockerd",
	} {
		if !Touches(c) {
			t.Errorf("not caught: %s", c)
		}
	}
	for _, c := range []string{
		"systemctl is-active docker",
		"systemctl is-active --quiet docker.service",
		"systemctl is-enabled --quiet docker.service",
		"systemctl show -p ActiveState docker.service",
		"sudo -n systemctl daemon-reload",
		"sudo -n systemctl enable --now flocc-docker-user.service",
		"sudo -n systemctl restart flocc-docker-user.service",
		"service docker status",
		"pidof dockerd",
		"After=docker.service",
	} {
		if Touches(c) {
			t.Errorf("wrongly caught: %s", c)
		}
	}
}
