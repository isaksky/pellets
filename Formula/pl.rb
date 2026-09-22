class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.4.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.4.0/pellets_0.4.0_darwin_arm64.tar.gz"
    sha256 "0fa5bc96b8ee0b919c6a44ca6583ab608e6c7af125256d8da7f2b6443fa82885"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.4.0/pellets_0.4.0_darwin_amd64.tar.gz"
    sha256 "856b94ae324b15f906ffaf0b306ebc384fe170ce69f0c73c537bc9d615c7b244"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
