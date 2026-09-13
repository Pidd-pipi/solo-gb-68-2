package services

// 测试基础设施：TestMain 启动一个用户态嵌入式 PostgreSQL 15（与生产
// postgres:15-alpine 同大版本），加载项目真实的 database/init.sql 建表，
// 供本包内所有测试复用。无需 Docker、无需 root、无需外部数据库，
// `go test ./internal/services/` 即可重复运行。

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"irrigation/pkg/database"
)

// initSQLPath 相对于本包目录（backend/internal/services）的建表脚本位置。
const initSQLPath = "../../../database/init.sql"

func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate free port: %w", err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}

func TestMain(m *testing.M) {
	code, err := runWithPostgres(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test postgres setup failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runWithPostgres(m *testing.M) (int, error) {
	port, err := freePort()
	if err != nil {
		return 1, err
	}

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V15).
		Port(port).
		Username("postgres").
		Password("postgres").
		Database("postgres"))
	if err := pg.Start(); err != nil {
		return 1, fmt.Errorf("start embedded postgres: %w", err)
	}

	dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=postgres password=postgres dbname=postgres sslmode=disable", port)
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true, // 允许一次 Exec 执行 init.sql 中的多条语句
	}), &gorm.Config{})
	if err != nil {
		_ = pg.Stop()
		return 1, fmt.Errorf("connect embedded postgres: %w", err)
	}

	initSQL, err := os.ReadFile(filepath.Clean(initSQLPath))
	if err != nil {
		_ = pg.Stop()
		return 1, fmt.Errorf("read %s: %w", initSQLPath, err)
	}
	if err := db.Exec(string(initSQL)).Error; err != nil {
		_ = pg.Stop()
		return 1, fmt.Errorf("apply init.sql: %w", err)
	}

	database.DB = db

	code := m.Run()
	_ = pg.Stop()
	return code, nil
}
