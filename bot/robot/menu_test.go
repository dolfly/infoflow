package robot

import "testing"

func validMenuItem() MenuItem {
	return MenuItem{
		Name: "Docs",
		Action: &MenuAction{
			DataType: "url",
			LinkConfig: &MenuLinkConfig{
				PCURL: "https://example.com/pc", PCBrowser: 1,
				MobileURL: "https://example.com/mobile", MobileOpenType: 0,
			},
		},
	}
}

func TestValidateMenusRejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		name  string
		menus []MenuItem
	}{
		{name: "empty", menus: nil},
		{name: "leaf without action", menus: []MenuItem{{Name: "Docs"}}},
		{name: "parent with action", menus: []MenuItem{{Name: "Tools", SubMenus: []MenuItem{validMenuItem()}, Action: &MenuAction{DataType: "push"}}}},
		{name: "third level", menus: []MenuItem{{Name: "Tools", SubMenus: []MenuItem{{Name: "More", SubMenus: []MenuItem{validMenuItem()}}}}}},
		{name: "unknown action", menus: []MenuItem{{Name: "Docs", Action: &MenuAction{DataType: "image"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMenus(test.menus, 0); err == nil {
				t.Fatal("validateMenus returned nil")
			}
		})
	}
}

func TestValidateMenusAcceptsPushAndURLLeaves(t *testing.T) {
	menus := []MenuItem{
		{Name: "Help", Action: &MenuAction{DataType: "push"}},
		{Name: "Docs", Action: validMenuItem().Action},
	}
	if err := validateMenus(menus, 0); err != nil {
		t.Fatalf("validateMenus error = %v", err)
	}
}
