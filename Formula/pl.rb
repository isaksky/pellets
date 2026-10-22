class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.6.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.6.0/pellets_0.6.0_darwin_arm64.tar.gz"
    sha256 "25abc18a44692c7dc00f50df7096e9b289c7d21781907d2cae0c192d74c47042"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.6.0/pellets_0.6.0_darwin_amd64.tar.gz"
    sha256 "1cbb4495c275721fd23655e288fdcb9aa32b6847aa0c5b9bdd721f77e83b32e1"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
