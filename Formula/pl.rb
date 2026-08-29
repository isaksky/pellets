class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.1.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.1.0/pellets_0.1.0_darwin_arm64.tar.gz"
    sha256 "31cb84d30ce481c7fa3d856aee882513242f2896a0d982b8cadbce301ff0869d"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.1.0/pellets_0.1.0_darwin_amd64.tar.gz"
    sha256 "76751286cca746a6694b3b552ae4a657d63650f89d0df32939fa7459569d9f52"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
