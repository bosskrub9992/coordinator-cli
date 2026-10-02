package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	Title   = "coord"
	Timeout = 10 * time.Second
	envText = "COORD_NOTIFY_TEXT"
	envHead = "COORD_NOTIFY_TITLE"
)

const windowsToast = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null; ` +
	`$x = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02); ` +
	`$t = $x.GetElementsByTagName('text'); ` +
	`$t.Item(0).AppendChild($x.CreateTextNode($env:COORD_NOTIFY_TITLE)) > $null; ` +
	`$t.Item(1).AppendChild($x.CreateTextNode($env:COORD_NOTIFY_TEXT)) > $null; ` +
	`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($x))`

const macScript = `display notification (system attribute "COORD_NOTIFY_TEXT") with title (system attribute "COORD_NOTIFY_TITLE")`

func Command(ctx context.Context, title, text string) (*exec.Cmd, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("the notification text is empty")
	}
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.CommandContext(ctx, "osascript", "-e", macScript)
	case "windows":
		c = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", windowsToast)
	default:
		c = exec.CommandContext(ctx, "notify-send", "--app-name", Title, title, text)
	}
	c.Env = append(os.Environ(), envHead+"="+title, envText+"="+text)
	return c, nil
}

type Sender interface {
	Send(title, text string) error
	Fire(title, text string)
}

type Desktop struct{}

func (Desktop) Send(title, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	c, err := Command(ctx, title, text)
	if err != nil {
		return err
	}
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("send the desktop notification with %s: %w %s", c.Path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (Desktop) Fire(title, text string) {
	c, err := Command(context.Background(), title, text)
	if err != nil {
		return
	}
	if c.Start() == nil {
		go c.Wait()
	}
}
