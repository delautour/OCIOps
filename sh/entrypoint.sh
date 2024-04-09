#!/bin/sh

echo "Starting entrypoint.sh..."

ssh-keygen -A
# set up ssh config
cat <<EOF > /etc/ssh/sshd_config
Port 22
HostKey /etc/ssh/ssh_host_ed25519_key
HostKey /etc/ssh/ssh_host_rsa_key
PermitRootLogin no
PasswordAuthentication no
PrintMotd no
SyslogFacility AUTH
EOF

# set up ssh for git user
mkdir -p /home/git/.ssh
ssh-keygen -t ed25519 -f /home/git/.ssh/id_ed25519 -N ""
cat /home/git/.ssh/id_ed25519.pub > /home/git/.ssh/authorized_keys
chown -R git:git /home/git
chmod 755 /home/git
# Correct the permissions for .ssh directory and authorized_keys file
chmod 700 /home/git/.ssh
chmod 600 /home/git/.ssh/authorized_keys

# start sshd
echo "Starting SSH daemon..."
#/usr/sbin/sshd -D &
#sshd_pid=$!

# Function to forward signals to the operator
forward_signal() {
  echo "Received signal $1, forwarding to operator..."

  kill "-$1" "$operator_pid"
#  kill "-$1" "$sshd_pid"
}

# Catch the signals and forward them to the operator
for signal in HUP INT QUIT TERM; do
  trap "forward_signal $signal" "$signal"
done

echo "Starting operator..."
# Start operator in the background and get its PID
/bin/operator &
operator_pid=$!

# Wait for the operator to exit
wait "$operator_pid"
#wait "$sshd_pid"