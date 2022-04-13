package itercal

import (
	"context"
	"fmt"
	"time"

	directory "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/calendar/v3"
)

func ForEachEvent(ctx context.Context, srv *calendar.Service, calendarId string, start, end time.Time, f func(*calendar.Event) error) error {
	ec := srv.Events.List(calendarId).
		Context(ctx).
		ShowDeleted(false).SingleEvents(true).
		TimeMin(start.Format(time.RFC3339)).
		TimeMax(end.Format(time.RFC3339)).
		OrderBy("startTime")
	return ec.Pages(ctx, func(events *calendar.Events) error {
		for _, item := range events.Items {
			if err := f(item); err != nil {
				return err
			}
		}
		return nil
	})
}

func ForEachBuilding(ctx context.Context, srv *directory.Service, f func(b *directory.Building) error) error {
	bc := srv.Resources.Buildings.List("my_customer").Context(ctx)
	return bc.Pages(ctx, func(buildings *directory.Buildings) error {
		for _, b := range buildings.Buildings {
			if err := f(b); err != nil {
				return err
			}
		}
		return nil
	})
}

func ForEachResourceInBuilding(ctx context.Context, srv *directory.Service, buildingId string, f func(r *directory.CalendarResource) error) error {
	qstr := "resourceCategory=CONFERENCE_ROOM"
	if buildingId != "" {
		qstr = fmt.Sprintf("buildingId=%s AND %s", buildingId, qstr)
	}
	rc := srv.Resources.Calendars.List("my_customer").Context(ctx).Query(qstr)
	return rc.Pages(ctx, func(calendars *directory.CalendarResources) error {
		for _, c := range calendars.Items {
			if err := f(c); err != nil {
				return err
			}
		}
		return nil
	})
}
